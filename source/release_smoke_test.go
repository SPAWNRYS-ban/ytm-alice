package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Explicit network check: no account or device code is requested.
func TestPublicReleaseSmoke(t *testing.T) {
	if os.Getenv("YTM_PUBLIC_RELEASE_SMOKE") != "1" {
		t.Skip("opt-in public provider check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c := newCatalog()
	if _, err := c.tvClientCredentials(ctx); err != nil {
		t.Fatal("TV sign-in client discovery:", err)
	}
	for _, query := range []string{"Yowai The Maid", "йовай зе мейд"} {
		tracks, err := c.playLookup(ctx, query)
		if err != nil || len(tracks) == 0 {
			t.Fatalf("public video fallback %q: %v", query, err)
		}
		if !strings.Contains(strings.ToLower(tracks[0].Title), "chasm") {
			t.Fatalf("%q: unrelated first result: %s", query, tracks[0].Title)
		}
		t.Logf("%q: %d relevant tracks; first: %s — %s", query, len(tracks), tracks[0].Artists, tracks[0].Title)
	}
}

func TestPublicDeviceCodeStartAndCancel(t *testing.T) {
	if os.Getenv("YTM_PUBLIC_RELEASE_SMOKE") != "1" {
		t.Skip("opt-in device-flow endpoint check; no account authorized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c := newCatalog()
	defer c.cancelTVLogin()
	state, err := c.startTVLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "waiting" || state.UserCode == "" || state.VerificationURL == "" {
		t.Fatal("device code was not issued")
	}
	c.cancelTVLogin()
	if c.tvLoginStatus().Status != "idle" || c.credentials() != nil {
		t.Fatal("cancelled flow changed account")
	}
	t.Log("Public device code issued and cancelled; no account authorized")
}
