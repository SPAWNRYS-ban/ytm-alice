package main

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
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
	added := newRadioTracks(s.state.Queue, task.tracks)
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
	a, query, ok := voiceAction(text)
	// With youtube_only all music goes through Alice's answer, which also
	// fixes misheard names («поставь mister brightside» found nothing by text,
	// «включи» found «The Killers — Mr. Brightside» via the catalog). The text
	// route keeps «ютуб» phrases, likes and player controls.
	if ok && a == "play" && settings.YouTubeOnly && !mentionsYouTube(q) && query != "мои лайки" {
		return "", "", false
	}
	return a, query, ok
}

// Only complete, known RGB rings are addressed; never clocks, microphone
// indicators, logos or ambilight. Mini 1 uses I2C and is intentionally excluded.
func playbackLEDPaths(root string) []string {
	for _, profile := range []struct {
		name  string
		count int
	}{{"micro", 3}, {"mini2", 6}, {"plum", 3}, {"bergamot", 6},
		{"cucumber", 24}, {"pickle", 16}, {"mango", 4}, {"station2", 88}, {"orion", 36}} {
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

// Firmware animations own the ring through ledfb-ring. Brightness writes alone
// race those animations and cause flashes (#13). Temporarily take each trigger,
// then restore it before handing the ring back to Alice or on shutdown.
type playbackIndicator struct {
	paths    []string
	triggers map[string]string
}

func activeLEDTrigger(raw []byte) string {
	for _, value := range strings.Fields(string(raw)) {
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			return strings.Trim(value, "[]")
		}
	}
	return strings.TrimSpace(string(raw))
}

func (led *playbackIndicator) update(dark bool) {
	if led.triggers == nil {
		led.triggers = map[string]string{}
	}
	if !dark {
		for path, original := range led.triggers {
			current, err := os.ReadFile(path)
			// Another owner may have changed the trigger in the meantime.
			if err == nil && activeLEDTrigger(current) == "none" {
				if os.WriteFile(path, []byte(original), 0) != nil {
					continue
				}
			}
			delete(led.triggers, path)
		}
		return
	}
	for _, brightness := range led.paths {
		trigger := filepath.Join(filepath.Dir(brightness), "trigger")
		if _, owned := led.triggers[trigger]; owned {
			continue // The detached firmware driver cannot relight this node.
		}
		raw, err := os.ReadFile(trigger)
		if err != nil {
			continue
		}
		original := activeLEDTrigger(raw)
		if original == "" {
			continue
		}
		if err := os.WriteFile(trigger, []byte("none"), 0); err != nil {
			continue
		}
		led.triggers[trigger] = original
		if err := os.WriteFile(brightness, []byte("0"), 0); err != nil {
			_ = os.WriteFile(trigger, []byte(original), 0)
			delete(led.triggers, trigger)
		}
	}
}

func suppressPlaybackLED(enabled bool, status string, aliceIdle bool) bool {
	if enabled || !aliceIdle {
		return false
	}
	// Keep ownership across track changes, seeks and CDN rebuffering.
	return status == "playing" || status == "buffering" || status == "searching"
}

func (s *Server) indicatorLoop(ctx context.Context) {
	led := playbackIndicator{paths: playbackLEDPaths("/sys/class/leds")}
	if len(led.paths) == 0 {
		return
	}
	defer led.update(false)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			dark := suppressPlaybackLED(s.settingsLocked().PlaybackLED, s.state.Status, s.aliceIdle.Load())
			s.mu.Unlock()
			led.update(dark)
		}
	}
}

// newRadioTracks returns radio tracks not already queued. YouTube radio often
// lists a song twice, as the audio track and as its music video under another
// ID; a song is the same when its main artist and its title without a
// "(Official Video)"-style suffix match. Of two versions the non-video wins.
func newRadioTracks(queue, radio []Track) []Track {
	seenID := map[string]bool{}
	seenSong := map[string]bool{}
	for _, track := range queue {
		seenID[track.ID] = true
		seenSong[songKey(track)] = true
	}
	added := []Track{}
	at := map[string]int{}
	for _, track := range radio {
		key := songKey(track)
		if seenID[track.ID] || seenSong[key] {
			continue
		}
		seenID[track.ID] = true
		if i, ok := at[key]; ok {
			if musicVideo(added[i].Title) && !musicVideo(track.Title) {
				added[i] = track
			}
			continue
		}
		at[key] = len(added)
		added = append(added, track)
	}
	return added
}

var videoSuffix = regexp.MustCompile(`(?i)\s*[\(\[][^\)\]]*\b(official|video|clip|lyrics?|audio|visuali[sz]er|hd|4k|remaster(ed)?)\b[^\)\]]*[\)\]]`)

var videoMarker = regexp.MustCompile(`(?i)[\(\[][^\)\]]*\b(video|clip|lyrics?|visuali[sz]er|live)\b`)

// musicVideo reports a clip/lyric/live upload rather than the audio track.
func musicVideo(title string) bool {
	return videoMarker.MatchString(title)
}

func songKey(track Track) string {
	title := strings.Join(searchWords(videoSuffix.ReplaceAllString(track.Title, "")), " ")
	artist := strings.ToLower(track.Artists)
	for _, sep := range []string{",", "&", " и ", " feat", " ft.", " x "} {
		if i := strings.Index(artist, sep); i > 0 {
			artist = artist[:i]
		}
	}
	return strings.Join(searchWords(artist), " ") + "|" + title
}

// Templates are opt-in; existing plain announcements keep their wording.
func trackAnnouncement(template string, track Track) string {
	return strings.NewReplacer("{artist}", track.Artists, "{title}", track.Title).Replace(template)
}
