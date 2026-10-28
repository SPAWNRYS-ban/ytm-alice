package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestVoiceRouting(t *testing.T) {
	for _, q := range []string{"Алиса, включи на ютубе Daft Punk!", "включи в ютубе jazz", "Включи YouTube Music ambient"} {
		a, s, ok := voiceAction(q)
		if !ok || a != "play" || s == "" {
			t.Fatalf("not routed: %q", q)
		}
	}
	for _, q := range []string{"Алиса, какая погода?", "включи музыку", "включи мою волну", "включи на ютубе"} {
		if _, _, ok := voiceAction(q); ok {
			t.Fatalf("native request intercepted: %q", q)
		}
	}
	a, _, ok := voiceAction("Алиса, следующий трек")
	if !ok || a != "next" {
		t.Fatal("next command missing")
	}
}

func TestVoiceMusicShorthandAndNativeBoundaries(t *testing.T) {
	for _, tc := range []struct{ phrase, query string }{
		{"Алиса поставь sewerslvt newlove", "sewerslvt newlove"},
		{"Алиса, поставь Sewerslvt Newlove, пожалуйста!", "sewerslvt newlove"},
		{"поставь песню Sewerslvt Newlove", "sewerslvt newlove"},
		{"включи трек Sewerslvt Newlove", "sewerslvt newlove"},
		{"поставь на ютубе Sewerslvt Newlove", "sewerslvt newlove"},
		{"поставь альбом Random Access Memories", "альбом random access memories"},
		{"включи плейлист jazz", "плейлист jazz"},
		{"поставь песню таймер", "таймер"},
	} {
		a, query, ok := voiceAction(tc.phrase)
		if !ok || a != "play" || query != tc.query {
			t.Fatalf("%q => %q %q %v", tc.phrase, a, query, ok)
		}
	}
	for _, phrase := range []string{
		"поставь таймер на пять минут", "поставь пять минут", "поставь на завтра будильник",
		"поставь будильник на семь", "поставь напоминание", "поставь громкость на двадцать",
		"поставь двадцать градусов", "включи свет", "включи кухню", "включи радио",
		"поставь радио", "поставь сказку", "поставь музыку", "включи музыку", "включи мою волну",
		"поставь", "Алиса, какая погода?",
	} {
		if a, query, ok := voiceAction(phrase); ok {
			t.Fatalf("native request %q intercepted as %s %q", phrase, a, query)
		}
	}
	if a, _, ok := voiceAction("поставь на паузу"); !ok || a != "pause" {
		t.Fatal("shorthand shadowed pause control")
	}
}

func TestRelayBoundsChunksAndPreservesRanges(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefg"), 400000)
	c := newCatalog()
	calls := 0
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		span := r.Header.Get("Range")
		if strings.HasSuffix(span, "-") {
			t.Fatal("open-ended CDN range")
		}
		var start, end int64
		if _, e := fmt.Sscanf(span, "bytes=%d-%d", &start, &end); e != nil {
			t.Fatal(e)
		}
		if end-start+1 > 1<<20 {
			t.Fatal("unbounded CDN request")
		}
		calls++
		return &http.Response{StatusCode: 206, Body: io.NopCloser(bytes.NewReader(data[start : end+1])), Header: make(http.Header)}, nil
	})}
	s := &Server{catalog: c, player: &nativePlayer{token: "test-token", source: streamInfo{url: "https://example.googlevideo.com/a", mime: "audio/mp4", length: int64(len(data))}}}
	for _, span := range []string{"", "bytes=100-", "bytes=200-399", "bytes=-100"} {
		req := httptest.NewRequest("GET", "http://127.0.0.1/audio/test-token", nil)
		if span != "" {
			req.Header.Set("Range", span)
		}
		w := httptest.NewRecorder()
		s.relay(w, req)
		start, end, e := byteRange(span, int64(len(data)))
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(w.Body.Bytes(), data[start:end+1]) {
			t.Fatalf("wrong audio range %q", span)
		}
		if span != "" && w.Code != 206 {
			t.Fatal("range status lost")
		}
	}
	if calls < 6 {
		t.Fatal("relay did not split the whole stream")
	}
	for _, span := range []string{"bytes=99999999-", "bytes=20-10", "bytes=1-3,4-5", "bytes=-0"} {
		if _, _, e := byteRange(span, 100); e == nil {
			t.Fatalf("invalid range accepted: %s", span)
		}
	}
	req := httptest.NewRequest("HEAD", "http://127.0.0.1/audio/test-token", nil)
	w := httptest.NewRecorder()
	before := calls
	s.relay(w, req)
	if w.Code != 200 || w.Body.Len() != 0 || calls != before {
		t.Fatal("HEAD must use metadata without a CDN HEAD request")
	}
}
func TestIPCEnvelopeAndIdle(t *testing.T) {
	d := proto(rpc(8, pi(1, 3)))
	if d[253].n != 210 || string(proto(d[252].b)[7].b) != "AliceCapability" {
		t.Fatal("bad remoting envelope")
	}
	e := decodeAlice(cat(pb(252, cat(pi(1, 3), pb(2, []byte("включи на ютубе jazz")))), pi(253, 3)))
	if !e.HasState || e.State != 3 || e.Text != "включи на ютубе jazz" {
		t.Fatal("speech decode failed")
	}
	idle := decodeAlice(cat(pb(252, pi(5, 1)), pi(253, 3)))
	if !idle.HasState || idle.State != 0 {
		t.Fatal("omitted proto2 idle must decode as idle")
	}
	for _, b := range [][]byte{{0}, {8, 128}, {18, 200}, {15}} {
		if _, e := parseProto(b); e == nil {
			t.Fatalf("malformed protobuf accepted: %v", b)
		}
	}
}
func TestCatalogActualSearch(t *testing.T) {
	raw, e := os.ReadFile("testdata/search.json")
	if e != nil {
		t.Fatal(e)
	}
	var data any
	if json.Unmarshal(raw, &data) != nil {
		t.Fatal("fixture invalid")
	}
	tracks := parseTracks(data)
	if len(tracks) < 3 || tracks[0].ID != "4D7u5KF7SP8" || !strings.Contains(tracks[0].Artists, "Daft Punk") {
		t.Fatalf("catalogue lost ordered tracks: %#v", tracks)
	}
}
func TestPlaylistParsingDeduplicates(t *testing.T) {
	raw := `{"contents":{"unknownNewRenderer":{"contents":[{"playlistPanelVideoRenderer":{"videoId":"abcdefghijk","title":{"simpleText":"First"},"lengthText":{"simpleText":"1:02"},"longBylineText":{"runs":[{"text":"Artist"}]}}},{"playlistPanelVideoRenderer":{"videoId":"abcdefghijk","title":{"simpleText":"Duplicate"}}},{"playlistPanelVideoRenderer":{"videoId":"short","title":{"simpleText":"Broken"}}}]}}}`
	var data any
	json.Unmarshal([]byte(raw), &data)
	tracks := parseTracks(data)
	if len(tracks) != 1 || tracks[0].DurationMS != 62000 || tracks[0].Artists != "Artist" {
		t.Fatalf("playlist parse: %#v", tracks)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestStopCancelsPendingSearch(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		once.Do(func() { close(entered) })
		<-release
		body := `{"contents":[{"playlistPanelVideoRenderer":{"videoId":"abcdefghijk","title":{"simpleText":"Track"}}}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	s := &Server{catalog: c, state: State{Status: "stopped", Announce: false}}
	done := make(chan response, 1)
	go func() { done <- s.command(context.Background(), request{Action: "play", Query: "jazz"}) }()
	<-entered
	s.command(context.Background(), request{Action: "stop"})
	close(release)
	result := <-done
	if result.Error == "" || s.snapshot().Status != "stopped" || s.player != nil {
		t.Fatal("old search restarted stopped playback")
	}
}
func TestExternalLinksRejected(t *testing.T) {
	c := newCatalog()
	for _, q := range []string{"file:///etc/passwd", "http://127.0.0.1/secrets", "https://youtube.com.attacker.invalid/watch?v=abcdefghijk"} {
		if _, err := c.lookup(context.Background(), q); err == nil {
			t.Fatalf("accepted %q", q)
		}
	}
}

func TestStaleRetryDoesNotRestartStoppedPlayer(t *testing.T) {
	s := &Server{generation: 3, state: State{Status: "stopped", Queue: []Track{{ID: "abcdefghijk"}}}}
	old := uint64(2)
	s.startGuarded(0, false, 1, &old)
	if s.snapshot().Status != "stopped" || s.generation != 3 || s.player != nil {
		t.Fatal("stale retry restarted playback")
	}
}
