package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jsonReply(data any) *http.Response {
	raw, _ := json.Marshal(data)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}
}

func trackPage(first, count int) map[string]any {
	items := []any{}
	for i := first; i < first+count; i++ {
		items = append(items, map[string]any{"playlistPanelVideoRenderer": map[string]any{"videoId": fmt.Sprintf("%011d", i), "title": map[string]any{"simpleText": fmt.Sprint(i)}}})
	}
	return map[string]any{"contents": items}
}

func TestSettingsPersistAndLegacyPortMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			t.Setenv("YTM_ALICE_DATA", t.TempDir())
			if legacy {
				os.WriteFile(filepath.Join(dataDir(), "state.json"), []byte(`{}`), 0600)
			}
			s := &Server{}
			if err := s.loadSettings(); err != nil {
				t.Fatal(err)
			}
			want := defaultWebAddr
			if legacy {
				want = ":80"
			}
			if s.getSettings().WebAddr != want {
				t.Fatal("incorrect migration", s.getSettings())
			}
			r, err := configRequest([]string{"set", "web_addr", ":34567"})
			if err != nil {
				t.Fatal(err)
			}
			if out := s.command(context.Background(), r); out.Error != "" {
				t.Fatal(out.Error)
			}
			reloaded := &Server{}
			if err := reloaded.loadSettings(); err != nil {
				t.Fatal(err)
			}
			if reloaded.getSettings().WebAddr != ":34567" {
				t.Fatal("port did not survive restart")
			}
			info, _ := os.Stat(settingsPath())
			if info.Mode().Perm() != 0600 {
				t.Fatal("incorrect settings permissions")
			}
			bad := "bad-host:8008"
			out := s.settingsCommand(request{Action: "config-set", Settings: &SettingsPatch{WebAddr: &bad}})
			if out.Error == "" || s.getSettings().WebAddr != ":34567" {
				t.Fatal("invalid settings replaced working configuration")
			}
		})
	}
}

func TestVoiceRecommendationsAndCustomAliases(t *testing.T) {
	for _, phrase := range []string{"Алиса, включи музыку на ютубе!", "поставь музыку на ютубе", "включи рекомендации"} {
		a, q, ok := voiceAction(phrase)
		if !ok || a != "recommendations" || q != "" {
			t.Fatalf("%s => %s %s %v", phrase, a, q, ok)
		}
	}
	settings := defaultSettings()
	settings.VoiceCommands = map[string]string{"сыграй {query}": "play", "включи подборку": "recommendations", "замри": "pause"}
	s := &Server{settings: &settings}
	for _, tc := range []struct{ phrase, action, query string }{{"Алиса, сыграй Daft Punk", "play", "daft punk"}, {"замри", "pause", ""}, {"включи подборку", "recommendations", ""}} {
		a, q, ok := s.voiceAction(tc.phrase)
		if !ok || a != tc.action || q != tc.query {
			t.Fatalf("%+v: %s %s", tc, a, q)
		}
	}
	if _, _, ok := s.voiceAction("включи свет"); ok {
		t.Fatal("custom aliases intercepted smart home")
	}
}

func TestPlaylistLoadsAllPagesWithoutDuplicates(t *testing.T) {
	c := newCatalog()
	calls := 0
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if calls == 1 {
			if body["browseId"] != "VLPL_test" {
				t.Fatal(body)
			}
			page := trackPage(0, 60)
			page["continuations"] = []any{map[string]any{"nextContinuationData": map[string]any{"continuation": "page-2"}}}
			return jsonReply(page), nil
		}
		if body["continuation"] != "page-2" || body["browseId"] != nil {
			t.Fatal(body)
		}
		return jsonReply(trackPage(58, 62)), nil
	})}
	tracks, err := c.browse(context.Background(), "VLPL_test")
	if err != nil || len(tracks) != 120 || calls != 2 || tracks[119].ID != "00000000119" {
		t.Fatalf("%d tracks, %d calls: %v", len(tracks), calls, err)
	}
}

func TestRepeatedContinuationIsReportedInsteadOfTruncating(t *testing.T) {
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return jsonReply(map[string]any{"continuations": []any{map[string]any{"nextContinuationData": map[string]any{"continuation": "same"}}}}), nil
	})}
	if _, err := c.browse(context.Background(), "VLPL_test"); err == nil {
		t.Fatal("repeated token silently accepted")
	}
}

func TestPlaylistDoesNotFollowEndlessSuggestions(t *testing.T) {
	for _, next := range []bool{false, true} {
		t.Run(fmt.Sprint(next), func(t *testing.T) {
			c := newCatalog()
			calls := 0
			c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if calls == 1 {
					tracks := trackPage(0, 7)
					if next {
						tracks["continuations"] = []any{map[string]any{"nextContinuationData": map[string]any{"continuation": "tracks-2"}}}
					}
					return jsonReply(map[string]any{"contents": map[string]any{"sectionListRenderer": map[string]any{
						"contents":      []any{map[string]any{"musicPlaylistShelfRenderer": tracks}, map[string]any{"musicShelfRenderer": trackPage(1000, 10)}},
						"continuations": []any{map[string]any{"nextContinuationData": map[string]any{"continuation": "suggestions"}}},
					}}}), nil
				}
				if !next || calls != 2 || body["continuation"] != "tracks-2" {
					t.Fatalf("followed suggestions instead of playlist: %v", body)
				}
				return jsonReply(map[string]any{"continuationContents": map[string]any{"musicPlaylistShelfContinuation": trackPage(7, 3)}}), nil
			})}
			tracks, err := c.browse(context.Background(), "VLPL_test")
			want := 7
			if next {
				want = 10
			}
			if err != nil || len(tracks) != want {
				t.Fatalf("got %d tracks: %v", len(tracks), err)
			}
		})
	}
}

func TestExplicitTrackUsesMusicSessionOnly(t *testing.T) {
	c := newCatalog()
	c.auth = &Credentials{Cookie: "SAPISID=fixture-only", AuthUser: "0"}
	musicCalls, publicCalls := 0, 0
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "music.youtube.com" {
			publicCalls++
			if strings.Contains(r.Header.Get("Cookie"), "fixture-only") || r.Header.Get("Authorization") != "" {
				t.Fatal("session leaked to public resolver")
			}
			return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		musicCalls++
		if r.URL.Path != "/youtubei/v1/player" || !strings.Contains(r.Header.Get("Cookie"), "fixture-only") || r.Header.Get("Authorization") == "" {
			t.Fatal("missing authenticated Music request")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["racyCheckOk"] != true || body["contentCheckOk"] != true {
			t.Fatal("missing content acknowledgement")
		}
		return jsonReply(map[string]any{"playabilityStatus": map[string]any{"status": "OK"}, "streamingData": map[string]any{"adaptiveFormats": []any{map[string]any{"itag": 140, "url": "https://test.googlevideo.com/audio", "mimeType": "audio/mp4", "audioChannels": 2, "contentLength": "1024"}}}}), nil
	})}
	stream, err := c.resolve(context.Background(), "abcdefghijk")
	if err != nil || stream.length != 1024 || musicCalls != 1 || publicCalls != 0 {
		t.Fatalf("fallback failed: %v, %+v, calls=%d", err, stream, musicCalls)
	}
}

func TestMusicLoginRequiredHasActionableMessage(t *testing.T) {
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return jsonReply(map[string]any{"playabilityStatus": map[string]any{"status": "LOGIN_REQUIRED"}}), nil
	})}
	_, err := c.musicVideo(context.Background(), "abcdefghijk")
	if err == nil || !strings.Contains(err.Error(), "cookies") {
		t.Fatal(err)
	}
}

func TestRadioDoesNotReviveStoppedQueue(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return jsonReply(trackPage(0, 3)), nil
	})}
	s := &Server{catalog: c, generation: 7, state: State{Status: "playing", Queue: []Track{{ID: "00000000000"}}, Index: 0}}
	done := make(chan struct{})
	go func() { s.fillRadio(7, "00000000000"); close(done) }()
	<-entered
	s.command(context.Background(), request{Action: "stop"})
	close(release)
	<-done
	if s.snapshot().Status != "stopped" || len(s.snapshot().Queue) != 1 {
		t.Fatal("late radio changed stopped queue")
	}
}

func TestRadioAppendsWithoutReplacingPlaylistAndHonorsOff(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	calls := 0
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { calls++; return jsonReply(trackPage(1, 3)), nil })}
	s := &Server{catalog: c, generation: 5, state: State{Status: "playing", Queue: []Track{{ID: "00000000000"}, {ID: "00000000001"}}, Index: 1}}
	s.fillRadio(5, "00000000001")
	s.fillRadio(5, "00000000001")
	if len(s.snapshot().Queue) != 4 || calls != 1 || s.snapshot().Queue[0].ID != "00000000000" {
		t.Fatal("radio replaced playlist or duplicated tracks", s.snapshot())
	}
	settings := defaultSettings()
	settings.Radio = false
	s.settings = &settings
	s.state.Index = 3
	s.fillRadio(5, "00000000003")
	if calls != 1 {
		t.Fatal("disabled radio issued a network request")
	}
}

func TestShuffleKeepsCurrentTrackAndRestoresOrder(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	tracks := []Track{}
	for i := 0; i < 20; i++ {
		tracks = append(tracks, Track{ID: fmt.Sprintf("%011d", i)})
	}
	s := &Server{state: State{Status: "playing"}}
	s.installQueueLocked(tracks, 4)
	if out := s.shuffleQueue(true); out.Error != "" {
		t.Fatal(out.Error)
	}
	if s.snapshot().Index != 4 || s.snapshot().Queue[4].ID != tracks[4].ID || len(s.snapshot().Queue) != 20 {
		t.Fatal("shuffle interrupted current track")
	}
	s.shuffleQueue(false)
	for i, track := range s.snapshot().Queue {
		if track.ID != tracks[i].ID {
			t.Fatal("order was not restored")
		}
	}
}

func TestLEDRingDiscoveryNeverIncludesClockOrMicrophone(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 3; i++ {
		for _, color := range []string{"red", "green", "blue"} {
			path := filepath.Join(root, fmt.Sprintf("micro:%s:ring%d", color, i))
			os.MkdirAll(path, 0700)
			os.WriteFile(filepath.Join(path, "brightness"), []byte("255"), 0600)
		}
	}
	for _, name := range []string{"micro:red:microphone", "mini2:white:clock"} {
		path := filepath.Join(root, name)
		os.MkdirAll(path, 0700)
		os.WriteFile(filepath.Join(path, "brightness"), []byte("255"), 0600)
	}
	paths := playbackLEDPaths(root)
	if len(paths) != 9 {
		t.Fatal(paths)
	}
	for _, path := range paths {
		if !strings.Contains(path, "ring") {
			t.Fatal("non-ring LED included", path)
		}
	}
}

func TestConfiguredWebPortOverridesDefaultAndEnvTakesPrecedence(t *testing.T) {
	settings := defaultSettings()
	settings.WebAddr = ":34567"
	t.Setenv("YTM_ALICE_WEB_ADDR", "")
	if effectiveWebAddr(settings) != ":34567" {
		t.Fatal("persistent port ignored")
	}
	t.Setenv("YTM_ALICE_WEB_ADDR", "127.0.0.1:18081")
	if effectiveWebAddr(settings) != "127.0.0.1:18081" {
		t.Fatal("environment override ignored")
	}
}

func TestNativeCLIEmptyAliasesSurviveRPCSerialization(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	settings := defaultSettings()
	settings.VoiceCommands = map[string]string{"замри": "pause"}
	s := &Server{settings: &settings}
	r, err := configRequest([]string{"set", "voice_commands", "{}"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	var decoded request
	json.Unmarshal(raw, &decoded)
	result := s.command(context.Background(), decoded)
	if result.Error != "" || len(s.getSettings().VoiceCommands) != 0 {
		t.Fatal("CLI could not remove voice aliases", result.Error)
	}
}

func TestWebReportsBoundPortUntilRestart(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	t.Setenv("YTM_ALICE_WEB_ADDR", "127.0.0.1:0")
	s := &Server{catalog: newCatalog()}
	closeWeb, err := s.startWeb()
	if err != nil {
		t.Fatal(err)
	}
	defer closeWeb()
	before := s.settingsCommand(request{Action: "config-get"}).WebAddr
	if before == "127.0.0.1:0" {
		t.Fatal("actual port not reported")
	}
	next := ":34567"
	out := s.settingsCommand(request{Action: "config-set", Settings: &SettingsPatch{WebAddr: &next}})
	if out.Error != "" || out.WebAddr != before || out.Settings.WebAddr != next {
		t.Fatal("pending port advertised as running", out)
	}
}
