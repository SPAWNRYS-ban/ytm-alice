package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type lunaClient struct {
	runtime, base, token, id string
	http                     *http.Client
	selected                 atomic.Bool
	ready                    chan struct{}
}

type lunaStatus struct {
	Selected string `json:"selected"`
	Pending  *struct {
		ID string `json:"id"`
	} `json:"pending"`
	LastResult *struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
		Error   string `json:"error"`
	} `json:"last_result"`
}

func newLunaClient() (*lunaClient, error) {
	runtime, token := os.Getenv("LUNA_API_URL"), os.Getenv("LUNA_API_TOKEN")
	if runtime == "" && token == "" {
		return nil, nil
	}
	u, err := url.Parse(runtime)
	if err != nil || token == "" || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("некорректная конфигурация Luna")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("Luna должна работать на этой Станции")
	}
	const prefix = "/api/runtime/"
	if !strings.HasPrefix(u.Path, prefix) {
		return nil, errors.New("неверный runtime URL Luna")
	}
	id := strings.TrimPrefix(u.Path, prefix)
	if id == "" || strings.ContainsAny(id, "/?#%") {
		return nil, errors.New("неверный ID плагина Luna")
	}
	return &lunaClient{runtime: strings.TrimRight(runtime, "/"), base: u.Scheme + "://" + u.Host, token: token, id: id,
		http: &http.Client{Timeout: 2 * time.Second}, ready: make(chan struct{})}, nil
}

func (l *lunaClient) request(ctx context.Context, endpoint string, body any, result any) error {
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	return l.requestMethod(ctx, method, endpoint, body, result)
}

func (l *lunaClient) requestMethod(ctx context.Context, method, endpoint string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	address := l.base + endpoint
	if !strings.HasPrefix(endpoint, "/api/") {
		address = l.runtime + endpoint
	}
	r, err := http.NewRequestWithContext(ctx, method, address, reader)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if !strings.HasPrefix(endpoint, "/api/") {
		r.Header.Set("X-Luna-Token", l.token)
	}
	res, err := l.http.Do(r)
	if err != nil {
		return errors.New("Luna недоступна")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(data) > 65536 {
		return errors.New("некорректный ответ Luna")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Do not expose request headers or runtime tokens in errors/logs.
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if len(failure.Error) > 250 {
			failure.Error = "ошибка API"
		}
		return fmt.Errorf("Luna: HTTP %d: %s", res.StatusCode, failure.Error)
	}
	if result != nil && len(data) > 0 {
		return json.Unmarshal(data, result)
	}
	return nil
}

func (l *lunaClient) operation(ctx context.Context, endpoint string, body any) error {
	ctx, cancel := context.WithTimeout(ctx, 5500*time.Millisecond)
	defer cancel()
	var accepted struct {
		ID string `json:"id"`
	}
	if err := l.request(ctx, endpoint, body, &accepted); err != nil {
		return err
	}
	if accepted.ID == "" {
		return errors.New("Luna не вернула ID операции")
	}
	// Once accepted, Luna owns the operation. Finish observing it even if the
	// HTTP caller disconnects, so a following pause cannot race a late resume.
	observe, done := context.WithTimeout(context.WithoutCancel(ctx), 5500*time.Millisecond)
	defer done()
	ctx = observe
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status lunaStatus
		if err := l.request(ctx, "/api/media", nil, &status); err != nil {
			return err
		}
		l.selected.Store(status.Selected == l.id)
		if status.LastResult != nil && status.LastResult.ID == accepted.ID {
			if status.LastResult.Success {
				return nil
			}
			return fmt.Errorf("Luna: %s", status.LastResult.Error)
		}
		if status.Pending == nil || status.Pending.ID != accepted.ID {
			return errors.New("Luna: результат операции неизвестен")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *lunaClient) activate(ctx context.Context) error {
	select {
	case <-l.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	return l.operation(ctx, "/api/media/select", map[string]string{"provider": l.id})
}

func (s *Server) command(ctx context.Context, r request) response {
	if s.luna == nil {
		return s.execute(ctx, r)
	}
	switch r.Action {
	case "play", "select", "jump", "next", "previous", "pause", "resume", "toggle", "stop":
		return s.lunaCommand(ctx, r)
	default:
		return s.execute(ctx, r)
	}
}

// Preparing a new intent can be interrupted by pause or another play request.
func (s *Server) newIntent(ctx context.Context) (context.Context, context.CancelFunc, uint64) {
	s.intentMu.Lock()
	defer s.intentMu.Unlock()
	if s.intentCancel != nil {
		s.intentCancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	s.intent++
	s.intentCancel = cancel
	return ctx, cancel, s.intent
}

func (s *Server) lunaCommand(parent context.Context, r request) response {
	ctx, cancel, intent := s.newIntent(parent)
	defer cancel()
	var active lunaStatus
	if err := s.luna.request(ctx, "/api/media", nil, &active); err != nil {
		return response{Error: err.Error()}
	}
	s.luna.selected.Store(active.Selected == s.luna.id)
	var tracks []Track
	index := 0
	var err error
	if r.Action == "play" {
		tracks, err = s.catalog.lookup(ctx, r.Query)
		if err != nil {
			return s.catalogError(err)
		}
	} else if r.Action == "select" || r.Action == "jump" {
		s.mu.Lock()
		if r.Action == "select" {
			tracks = append([]Track(nil), s.latestSearch...)
		} else {
			tracks = append([]Track(nil), s.state.Queue...)
		}
		s.mu.Unlock()
		index = r.Index - 1
		if index < 0 || index >= len(tracks) || (r.Query != "" && r.Query != tracks[index].ID) {
			return response{Error: "список изменился или номер вне списка"}
		}
	}
	// Expensive YouTube resolution is outside Luna's five-second transport lease.
	if len(tracks) > 0 {
		if _, err = s.catalog.resolve(ctx, tracks[index].ID); err != nil {
			return s.catalogError(err)
		}
	} else if r.Action == "resume" || r.Action == "next" || r.Action == "previous" || r.Action == "toggle" {
		s.mu.Lock()
		i := s.state.Index
		if r.Action == "next" {
			i++
		}
		if r.Action == "previous" {
			i--
		}
		var id string
		if i >= 0 && i < len(s.state.Queue) {
			id = s.state.Queue[i].ID
		}
		need := s.player == nil || r.Action == "next" || r.Action == "previous"
		s.mu.Unlock()
		if need && id != "" && (s.luna.selected.Load() || active.Selected == "") {
			if _, err = s.catalog.resolve(ctx, id); err != nil {
				return s.catalogError(err)
			}
		}
	}
	s.coordinatorMu.Lock()
	defer s.coordinatorMu.Unlock()
	if ctx.Err() != nil {
		return response{Error: "запрос заменён более новым"}
	}
	s.intentMu.Lock()
	current := s.intent == intent
	if current {
		s.intentCancel = nil
	}
	s.intentMu.Unlock()
	if !current {
		return response{Error: "запрос заменён более новым"}
	}
	if len(tracks) > 0 {
		if err = s.setPlayback(ctx, "paused"); err != nil {
			return response{Error: err.Error()}
		}
		s.mu.Lock()
		s.stopLocked()
		s.state.Queue = tracks
		s.state.Index = index
		s.state.PositionMS = 0
		s.state.Status = "paused"
		s.state.Error = ""
		s.saveLocked()
		s.mu.Unlock()
		if err = s.waitPlayback(ctx, "paused"); err != nil {
			return response{Error: err.Error()}
		}
		if err = s.luna.activate(ctx); err != nil {
			return response{Error: err.Error()}
		}
		if s.snapshot().Announce {
			s.speechMu.Lock()
			err = speak("Включаю")
			s.speechMu.Unlock()
			if err != nil {
				log.Print("native announcement not confirmed")
			}
		}
		err = s.luna.operation(ctx, "/api/media/command", map[string]string{"action": "resume"})
		if err == nil && r.Action == "play" && !isLikedQuery(r.Query) && !strings.Contains(r.Query, "://") && !strings.HasPrefix(r.Query, "альбом ") && !strings.HasPrefix(r.Query, "плейлист ") {
			s.mu.Lock()
			generation := s.generation
			s.mu.Unlock()
			go s.fillRadio(generation, tracks[index].ID)
		}
	} else if r.Action == "stop" {
		if !s.luna.selected.Load() {
			return response{Error: "выбран другой источник; команда stop относится к YouTube Music"}
		}
		result := s.execute(ctx, r)
		if result.Error != "" {
			return result
		}
		err = s.waitPlayback(ctx, "paused")
	} else {
		action := r.Action
		if action == "toggle" {
			var status struct {
				Selected  string                       `json:"selected"`
				Providers []struct{ ID, State string } `json:"providers"`
			}
			if err = s.luna.request(ctx, "/api/media", nil, &status); err != nil {
				return response{Error: err.Error()}
			}
			action = "resume"
			for _, p := range status.Providers {
				if p.ID == status.Selected && p.State == "playing" {
					action = "pause"
				}
			}
		}
		if action == "resume" && active.Selected == "" {
			if err = s.luna.activate(ctx); err != nil {
				return response{Error: err.Error()}
			}
		}
		err = s.luna.operation(ctx, "/api/media/command", map[string]string{"action": action})
	}
	if err != nil {
		return response{Error: err.Error()}
	}
	return response{State: ptrState(s.snapshot())}
}

func (s *Server) lunaLoop(ctx context.Context) {
	l := s.luna
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	lastReport, lastStatus := time.Time{}, time.Time{}
	ready := false
	commands := make(chan struct{}, 1)
	for ctx.Err() == nil {
		if time.Since(lastReport) >= 500*time.Millisecond {
			if err := l.request(ctx, "/provider/state", map[string]string{"state": s.playback()}, nil); err == nil {
				lastReport = time.Now()
				if !ready {
					close(l.ready)
					ready = true
					log.Print("Luna media provider ready")
				}
			}
		}
		if time.Since(lastStatus) >= 250*time.Millisecond {
			var status lunaStatus
			if l.request(ctx, "/api/media", nil, &status) == nil {
				l.selected.Store(status.Selected == l.id)
				lastStatus = time.Now()
			}
			if !lastStatus.IsZero() && (time.Since(lastStatus) > 2*time.Second || !l.selected.Load()) && s.playback() == "playing" {
				_ = s.setPlayback(ctx, "paused")
			}
		}
		var delivery *struct {
			ID      string `json:"id"`
			Action  string `json:"action"`
			Expires int64  `json:"expires_in_ms"`
		}
		if l.request(ctx, "/provider/next", struct{}{}, &delivery) == nil && delivery != nil {
			select {
			case commands <- struct{}{}:
				go func(id, action string, expires int64) {
					defer func() { <-commands }()
					if expires < 150 || expires > 5000 {
						_ = l.request(ctx, "/provider/result", map[string]any{"id": id, "success": false, "state": s.playback()}, nil)
						return
					}
					deadline, cancel := context.WithTimeout(ctx, time.Duration(expires)*time.Millisecond-100*time.Millisecond)
					defer cancel()
					res := s.execute(deadline, request{Action: action})
					wanted := "playing"
					if action == "pause" {
						wanted = "paused"
					}
					err := error(nil)
					if res.Error != "" {
						err = errors.New(res.Error)
					} else {
						err = s.waitPlayback(deadline, wanted)
					}
					if err != nil {
						s.mu.Lock()
						s.stopLocked()
						s.state.Status = "paused"
						s.saveLocked()
						s.mu.Unlock()
					}
					_ = l.request(ctx, "/provider/result", map[string]any{"id": id, "success": err == nil, "state": s.playback()}, nil)
				}(delivery.ID, delivery.Action, delivery.Expires)
			default:
				_ = l.request(ctx, "/provider/result", map[string]any{"id": delivery.ID, "success": false, "state": "unknown"}, nil)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
