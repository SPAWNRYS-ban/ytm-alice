package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestUnavailableQueueTerminatesWithoutRadio(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	c := newCatalog()
	c.playerSTS = 20761
	c.auth = &Credentials{Cookie: "SAPISID=fixture-only", AuthUser: "0"}
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return jsonReply(map[string]any{"playabilityStatus": map[string]any{"status": "UNPLAYABLE"}}), nil
	})}
	options := defaultSettings()
	options.AnnounceErrors = false
	s := &Server{catalog: c, settings: &options, state: State{Queue: []Track{{ID: "aaaaaaaaaaa"}, {ID: "bbbbbbbbbbb"}, {ID: "ccccccccccc"}}}}
	s.start(0, false)
	deadline := time.After(5 * time.Second)
	for {
		state := s.snapshot()
		if state.Status == "error" {
			if state.Index != 2 || len(state.Queue) != 3 {
				t.Fatalf("queue stopped before exhausting unavailable tracks: %+v", state)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("unavailable queue did not terminate")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestUnavailableClassificationAndAnnouncement(t *testing.T) {
	c := newCatalog()
	c.playerSTS = 20761
	for _, status := range []string{"LOGIN_REQUIRED", "CONTENT_CHECK_REQUIRED", "AGE_CHECK_REQUIRED", "UNPLAYABLE", "ERROR"} {
		c.http = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			return jsonReply(map[string]any{"playabilityStatus": map[string]any{"status": status}}), nil
		})}
		_, err := c.musicVideo(context.Background(), "aaaaaaaaaaa")
		var unavailable unavailableTrack
		if !errors.As(err, &unavailable) {
			t.Fatalf("%s must mark track unavailable: %v", status, err)
		}
	}
	if got := trackAnnouncement("Включаю {artist}. {title}", Track{Artists: "Artist", Title: "Title"}); got != "Включаю Artist. Title" {
		t.Fatal(got)
	}
	if got := trackAnnouncement("Включаю", Track{Title: "Title"}); got != "Включаю" {
		t.Fatal(got)
	}
}

func TestFinalVoiceCommandCancelsBeforeBusy(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	old := voiceIPCAddress
	voiceIPCAddress = func() string { return ln.Addr().String() }
	defer func() { voiceIPCAddress = old }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{state: State{Status: "buffering", Queue: []Track{{ID: "aaaaaaaaaaa"}}}}
	done := make(chan struct{})
	go func() { s.voiceLoop(ctx); close(done) }()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := frameWrite(conn, aliceStateFrame(1, "Алиса стоп", "stop-one", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := frameRead(conn); err != nil {
		t.Fatalf("no early cancellation: %v", err)
	}
	deadline := time.After(3 * time.Second)
	for s.snapshot().Status != "stopped" {
		select {
		case <-deadline:
			t.Fatal("stop command never ran")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("voice loop did not stop")
	}
}

func TestMusicFallbackDoesNotStealReorderedHomeCommands(t *testing.T) {
	for _, phrase := range []string{"включи в спальне свет", "поставь на завтра будильник", "запусти через минуту таймер"} {
		if query, ok := musicRequest(phrase); ok {
			t.Fatalf("home command became music: %q -> %q", phrase, query)
		}
	}
}
