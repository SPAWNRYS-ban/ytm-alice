package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	yt "github.com/kkdai/youtube/v2"
)

func requireRateLimit(t *testing.T, err error) {
	t.Helper()
	var limited rateLimited
	if !errors.As(err, &limited) {
		t.Fatalf("expected actionable 429, got %v", err)
	}
}

func TestMusic429StopsFallbackAndAllUncachedPlaybackRequests(t *testing.T) {
	c := newCatalog()
	c.auth = &Credentials{Cookie: "SAPISID=fixture-only", AuthUser: "0"}
	calls := 0
	c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "music.youtube.com" {
			t.Fatal("429 triggered a public fallback request")
		}
		return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	ctx := context.Background()
	_, err := c.resolve(ctx, "abcdefghijk")
	requireRateLimit(t, err)
	_, err = c.resolve(ctx, "bcdefghijkl")
	requireRateLimit(t, err)
	_, err = c.lookup(ctx, "https://youtu.be/cdefghijklm")
	requireRateLimit(t, err)
	if calls != 1 || c.limitBackoff != 2*time.Minute {
		t.Fatalf("cooldown not respected: calls=%d backoff=%s", calls, c.limitBackoff)
	}
	// Already resolved audio needs no metadata request and stays usable.
	c.streams["defghijklmn"] = streamInfo{url: "https://test.googlevideo.com/audio", until: time.Now().Add(time.Hour)}
	if _, err = c.resolve(ctx, "defghijklmn"); err != nil || calls != 1 {
		t.Fatal("cached playback blocked", err)
	}
	c.limitedUntil = time.Now().Add(-time.Second)
	_, err = c.resolve(ctx, "abcdefghijk")
	requireRateLimit(t, err)
	if calls != 2 || c.limitBackoff != 4*time.Minute {
		t.Fatal("repeated 429 did not extend the cooldown")
	}
	c.clearRateLimit() // A successful concurrent request must not erase a new 429.
	requireRateLimit(t, c.playbackLimit())
	for range 8 {
		c.noteRateLimit()
	}
	if c.limitBackoff != 30*time.Minute {
		t.Fatal("unbounded cooldown")
	}
	c.limitedUntil = time.Now().Add(-time.Second)
	c.clearRateLimit()
	if c.playbackLimit() != nil || c.limitBackoff != 0 {
		t.Fatal("successful recovery did not reset backoff")
	}
}

func TestDecoder429KeepsSharedClientAndActionableError(t *testing.T) {
	c := newCatalog()
	calls := 0
	c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	f := yt.Format{URL: "https://test.googlevideo.com/audio?n=fixture"}
	_, err := c.decipherURL(context.Background(), "abcdefghijk", f)
	requireRateLimit(t, err)
	client := c.ytClient
	if client == nil {
		t.Fatal("429 discarded the shared client")
	}
	_, err = c.playerProgram(context.Background(), "bcdefghijkl")
	requireRateLimit(t, err)
	if calls != 1 || c.ytClient != client {
		t.Fatal("blocked request hit the network or discarded cached state")
	}
}
