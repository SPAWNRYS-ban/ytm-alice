package main

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Server) installQueueLocked(tracks []Track, index int) {
	s.queueOrder = append([]Track(nil), tracks...)
	s.state.Queue = append([]Track(nil), tracks...)
	s.state.Index = index
	if index < 0 {
		s.state.Index = 0
	}
	s.queueCast = false
	if s.settingsLocked().Shuffle {
		rand.Shuffle(len(s.state.Queue), func(i, j int) { s.state.Queue[i], s.state.Queue[j] = s.state.Queue[j], s.state.Queue[i] })
		// Selecting a track must still start the selected track.
		if index >= 0 && index < len(tracks) {
			for i, track := range s.state.Queue {
				if track.ID == tracks[index].ID {
					s.state.Queue[0], s.state.Queue[i] = s.state.Queue[i], s.state.Queue[0]
					break
				}
			}
		}
		s.state.Index = 0
	}
}

func (s *Server) shuffleQueue(enabled bool) response {
	patch := SettingsPatch{Shuffle: &enabled}
	result := s.settingsCommand(request{Action: "config-set", Settings: &patch})
	if result.Error != "" {
		return result
	}
	s.mu.Lock()
	start := s.state.Index + 1
	if s.state.Status == "stopped" {
		start = 0
	}
	if start >= 0 && start < len(s.state.Queue) {
		tail := s.state.Queue[start:]
		if enabled {
			rand.Shuffle(len(tail), func(i, j int) { tail[i], tail[j] = tail[j], tail[i] })
		} else {
			order := map[string]int{}
			for i, t := range s.queueOrder {
				order[t.ID] = i
			}
			sort.SliceStable(tail, func(i, j int) bool { return order[tail[i].ID] < order[tail[j].ID] })
		}
		s.saveLocked()
	}
	s.mu.Unlock()
	value := s.snapshot()
	return response{State: &value}
}

// Prefetch only at the tail. A stopped/replaced queue and a disabled radio can
// never be revived by a slow recommendation request.
func (s *Server) fillRadio(g uint64, id string) {
	s.mu.Lock()
	if s.generation != g || s.queueCast || !s.settingsLocked().Radio || s.state.Index+1 < len(s.state.Queue) {
		s.mu.Unlock()
		return
	}
	task := s.radioLoad
	owner := task == nil || task.generation != g || task.id != id
	if owner {
		task = &radioLoad{generation: g, id: id, done: make(chan struct{})}
		s.radioLoad = task
	}
	s.mu.Unlock()
	if owner {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		task.tracks = s.catalog.radio(ctx, id)
		cancel()
		close(task.done)
	} else {
		<-task.done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != g || s.closing || s.queueCast || !s.settingsLocked().Radio {
		return
	}
	seen := map[string]bool{}
	for _, track := range s.state.Queue {
		seen[track.ID] = true
	}
	added := []Track{}
	for _, track := range task.tracks {
		if !seen[track.ID] {
			seen[track.ID] = true
			added = append(added, track)
		}
	}
	s.queueOrder = append(s.queueOrder, added...)
	if s.settingsLocked().Shuffle {
		rand.Shuffle(len(added), func(i, j int) { added[i], added[j] = added[j], added[i] })
	}
	s.state.Queue = append(s.state.Queue, added...)
	s.saveLocked()
}

type radioLoad struct {
	generation uint64
	id         string
	done       chan struct{}
	tracks     []Track
}

func (s *Server) finishQueue(g uint64, id string) {
	s.fillRadio(g, id)
	s.mu.Lock()
	if s.generation != g || s.closing {
		s.mu.Unlock()
		return
	}
	if s.state.Index+1 < len(s.state.Queue) {
		s.state.Index++
		s.state.PositionMS = 0
		s.mu.Unlock()
		s.startGuarded(0, false, 0, &g)
		return
	}
	s.state.Status = "stopped"
	s.state.PositionMS = 0
	s.saveLocked()
	s.mu.Unlock()
}

func (s *Server) notifyFailure(g uint64) {
	go func() {
		s.speechMu.Lock()
		defer s.speechMu.Unlock()
		s.mu.Lock()
		current := s.generation == g && s.state.Status == "error" && !s.closing && s.settingsLocked().AnnounceErrors
		s.mu.Unlock()
		if current {
			_ = speak("Не получилось включить музыку. Подробности доступны в панели.")
		}
	}()
}

func (s *Server) voiceAction(text string) (string, string, bool) {
	q := strings.TrimSpace(strings.TrimPrefix(normalize(text), "алиса "))
	q = strings.TrimSpace(strings.TrimSuffix(q, " пожалуйста"))
	settings := s.getSettings()
	// Prefer exact aliases, then the longest custom search prefix.
	if action, ok := settings.VoiceCommands[q]; ok {
		return action, "", true
	}
	prefixes := []string{}
	for phrase, action := range settings.VoiceCommands {
		if action == "play" {
			prefixes = append(prefixes, strings.TrimSuffix(phrase, "{query}"))
		}
	}
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	for _, prefix := range prefixes {
		if strings.HasPrefix(q, prefix) && strings.TrimSpace(q[len(prefix):]) != "" {
			return "play", strings.TrimSpace(q[len(prefix):]), true
		}
	}
	return voiceAction(text)
}

// Only the ring nodes of the two supported sysfs profiles are addressed. Do
// not touch the clock display, triggers, microphone LED or another model.
func playbackLEDPaths(root string) []string {
	for _, profile := range []struct {
		name  string
		count int
	}{{"micro", 3}, {"mini2", 6}} {
		paths := []string{}
		matches, _ := filepath.Glob(filepath.Join(root, profile.name+":*:ring*", "brightness"))
		for _, path := range matches {
			parts := strings.Split(filepath.Base(filepath.Dir(path)), ":")
			if len(parts) == 3 && (parts[1] == "red" || parts[1] == "green" || parts[1] == "blue") {
				paths = append(paths, path)
			}
		}
		if len(paths) == profile.count*3 {
			return paths
		}
	}
	return nil
}

func (s *Server) indicatorLoop(ctx context.Context) {
	paths := playbackLEDPaths("/sys/class/leds")
	if len(paths) == 0 {
		return
	}
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			dark := !s.settingsLocked().PlaybackLED && s.state.Status == "playing"
			s.mu.Unlock()
			// A fresh Alice dialog immediately regains the LED driver.
			if dark && s.aliceIdle.Load() {
				for _, path := range paths {
					_ = os.WriteFile(path, []byte("0"), 0)
				}
			}
		}
	}
}
