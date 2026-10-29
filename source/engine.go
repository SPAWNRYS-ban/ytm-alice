package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type nativePlayer struct {
	cmd       *exec.Cmd
	in        io.WriteCloser
	mu        sync.Mutex
	token     string
	source    streamInfo
	attempt   int
	confirmed string
	done      chan struct{}
	stopOnce  sync.Once
}

func (p *nativePlayer) send(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.in != nil {
		fmt.Fprintln(p.in, line)
	}
}
func (p *nativePlayer) stop() {
	p.stopOnce.Do(func() {
		p.send("stop")
		p.mu.Lock()
		if p.in != nil {
			p.in.Close()
			p.in = nil
		}
		p.mu.Unlock()
		time.AfterFunc(2*time.Second, func() {
			if p.cmd != nil && p.cmd.Process != nil {
				p.cmd.Process.Kill()
			}
		})
	})
}

func (s *Server) stopLocked() {
	s.generation++
	if s.prepCancel != nil {
		s.prepCancel()
		s.prepCancel = nil
	}
	if s.player != nil {
		s.retiring = append(s.retiring, s.player)
		s.player.stop()
		s.player = nil
	}
}
func (s *Server) start(offset int64, announce bool) {
	s.startGuarded(offset, announce, 0, nil)
}
func (s *Server) startGuarded(offset int64, announce bool, attempt int, expected *uint64) {
	s.mu.Lock()
	if expected != nil && s.generation != *expected {
		s.mu.Unlock()
		return
	}
	if s.closing || len(s.state.Queue) == 0 {
		s.mu.Unlock()
		return
	}
	if s.luna != nil && !s.luna.selected.Load() {
		s.state.Status = "paused"
		s.mu.Unlock()
		return
	}
	s.stopLocked()
	g := s.generation
	t := s.state.Queue[s.state.Index]
	s.state.Status = "buffering"
	s.state.Error = ""
	s.state.PositionMS = offset
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	s.prepCancel = cancel
	say := announce && s.state.Announce
	s.saveLocked()
	s.mu.Unlock()
	go func() {
		defer cancel()
		speechDone := make(chan struct{})
		if say {
			go func() {
				s.speechMu.Lock()
				if e := speak("Включаю"); e != nil {
					log.Print("native announcement not confirmed")
				}
				s.speechMu.Unlock()
				close(speechDone)
			}()
		} else {
			close(speechDone)
		}
		src, err := s.catalog.resolve(ctx, t.ID)
		select {
		case <-speechDone:
		case <-ctx.Done():
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.generation != g || s.closing {
			return
		}
		if err != nil {
			s.state.Status = "error"
			s.state.Error = err.Error()
			s.saveLocked()
			return
		}
		tokenBytes := make([]byte, 16)
		rand.Read(tokenBytes)
		token := hex.EncodeToString(tokenBytes)
		exe, _ := os.Executable()
		cmd := exec.Command(filepath.Join(filepath.Dir(exe), "ytm-alice-player"), "http://"+relayAddr+"/audio/"+token)
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/system/vendor/quasar:/system/vendor/quasar/libs")
		in, e := cmd.StdinPipe()
		if e != nil {
			s.failLocked("не удалось создать плеер")
			return
		}
		out, e := cmd.StdoutPipe()
		if e != nil {
			in.Close()
			s.failLocked("не удалось создать плеер")
			return
		}
		// The helper sees only a random loopback URL; never the signed CDN URL.
		playerLog, logErr := os.OpenFile("/tmp/ytm-alice-player.log", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if logErr == nil {
			cmd.Stderr = playerLog
			defer playerLog.Close()
		} else {
			cmd.Stderr = io.Discard
		}
		p := &nativePlayer{cmd: cmd, in: in, token: token, source: src, attempt: attempt, done: make(chan struct{})}
		s.player = p
		if e = cmd.Start(); e != nil {
			close(p.done)
			s.player = nil
			in.Close()
			s.failLocked("штатный GStreamer недоступен")
			return
		}
		go s.monitor(p, g, out, offset)
	}()
}
func (s *Server) failLocked(message string) {
	s.state.Status = "error"
	s.state.Error = message
	s.saveLocked()
}
func (s *Server) monitor(p *nativePlayer, g uint64, out io.ReadCloser, offset int64) {
	scanner := bufio.NewScanner(out)
	natural := false
	seeked := offset == 0
	for scanner.Scan() {
		var ev struct {
			Event      string `json:"event"`
			PositionMS int64  `json:"position_ms"`
		}
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		s.mu.Lock()
		if s.player != p || s.generation != g {
			s.mu.Unlock()
			break
		}
		switch ev.Event {
		case "playing":
			p.confirmed = "playing"
			s.state.Status = "playing"
			if !seeked {
				p.send("seek " + strconv.FormatInt(offset, 10))
				seeked = true
			}
			s.saveLocked()
		case "paused":
			if s.state.Status != "buffering" {
				p.confirmed = "paused"
				s.state.Status = "paused"
			}
		case "position":
			s.state.PositionMS = ev.PositionMS
		case "eos":
			natural = true
		case "error":
			s.failLocked("ошибка воспроизведения YouTube")
		}
		s.mu.Unlock()
	}
	err := p.cmd.Wait()
	close(p.done)
	p.mu.Lock()
	if p.in != nil {
		p.in.Close()
		p.in = nil
	}
	p.mu.Unlock()
	s.mu.Lock()
	if s.player != p || s.generation != g {
		s.mu.Unlock()
		return
	}
	s.player = nil
	if !natural || err != nil {
		if !s.closing {
			if p.attempt == 0 && s.state.PositionMS < 3000 {
				s.state.Status = "buffering"
				s.state.Error = ""
				s.mu.Unlock()
				go func() {
					s.catalog.discardStream(p.source)
					time.Sleep(300 * time.Millisecond)
					s.startGuarded(0, false, 1, &g)
				}()
				return
			}
			s.failLocked("аудиопоток прерван")
		}
		s.mu.Unlock()
		return
	}
	if s.state.Index+1 < len(s.state.Queue) {
		s.state.Index++
		s.state.PositionMS = 0
		s.mu.Unlock()
		s.start(0, false)
	} else {
		s.state.Status = "stopped"
		s.state.PositionMS = 0
		s.saveLocked()
		s.mu.Unlock()
	}
}

// Report observed output state, never an optimistic control request.
func (s *Server) playbackLocked() string {
	remaining := s.retiring[:0]
	for _, p := range s.retiring {
		if p.done == nil {
			continue
		}
		select {
		case <-p.done:
		default:
			remaining = append(remaining, p)
		}
	}
	s.retiring = remaining
	if len(remaining) != 0 {
		return "unknown"
	}
	if s.player != nil {
		if s.player.confirmed != "" {
			return s.player.confirmed
		}
		return "unknown"
	}
	if s.state.Status == "buffering" || s.state.Status == "searching" {
		return "unknown"
	}
	return "paused"
}

func (s *Server) playback() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playbackLocked()
}

func (s *Server) waitPlayback(ctx context.Context, wanted string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		actual := s.playbackLocked()
		failed := s.state.Status == "error"
		s.mu.Unlock()
		if actual == wanted && !(wanted == "playing" && failed) {
			return nil
		}
		if failed {
			return errors.New("плеер не подтвердил воспроизведение")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) setPlayback(ctx context.Context, wanted string) error {
	ctx, cancel := context.WithTimeout(ctx, 4200*time.Millisecond)
	defer cancel()
	s.mu.Lock()
	if s.playbackLocked() == wanted {
		s.mu.Unlock()
		return nil
	}
	p := s.player
	if wanted == "paused" && (p == nil || s.state.Status == "buffering" || s.state.Status == "searching") {
		s.stopLocked()
		s.state.Status = "paused"
		s.saveLocked()
		s.mu.Unlock()
		return s.waitPlayback(ctx, wanted)
	}
	if p == nil {
		if len(s.state.Queue) == 0 {
			s.mu.Unlock()
			return errors.New("очередь пуста")
		}
		s.mu.Unlock()
		s.start(0, false)
	} else {
		p.confirmed = ""
		if wanted == "paused" {
			p.send("pause")
		} else {
			p.send("resume")
		}
		s.mu.Unlock()
	}
	if err := s.waitPlayback(ctx, wanted); err != nil {
		s.mu.Lock()
		s.stopLocked()
		s.state.Status = "paused"
		s.saveLocked()
		s.mu.Unlock()
		return err
	}
	return nil
}
