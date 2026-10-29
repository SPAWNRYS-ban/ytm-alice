package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPauseDoesNotConfirmBeforeEngineEvent(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	done := make(chan struct{})
	s := &Server{state: State{Status: "playing"}, player: &nativePlayer{in: &inputBuffer{}, confirmed: "playing", done: done}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	result := s.command(ctx, request{Action: "pause"})
	if result.Error == "" {
		t.Fatal("unconfirmed pause acknowledged")
	}
	if s.playback() != "unknown" {
		t.Fatal("still-running helper reported as paused")
	}
	close(done)
	if s.playback() != "paused" {
		t.Fatal("exited helper still holds output")
	}
}

func TestLunaTransportDoesNotStealOtherProvider(t *testing.T) {
	var action string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Luna-Token") != "" {
			t.Error("runtime token sent to public endpoint")
		}
		switch r.URL.Path {
		case "/api/media":
			last := any(nil)
			if action != "" {
				last = map[string]any{"id": "operation", "success": true}
			}
			json.NewEncoder(w).Encode(map[string]any{"selected": "radio", "last_result": last})
		case "/api/media/command":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			action = body["action"]
			json.NewEncoder(w).Encode(map[string]string{"id": "operation"})
		case "/api/media/select":
			t.Error("transport selected YouTube over active radio")
			http.Error(w, "unexpected", 500)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := &Server{luna: &lunaClient{base: server.URL, id: lunaPluginID, http: server.Client()}, catalog: newCatalog()}
	result := s.command(context.Background(), request{Action: "resume"})
	if result.Error != "" || action != "resume" {
		t.Fatalf("transport failed: %+v action=%s", result, action)
	}
}

func TestLunaRuntimeTokenConfinedToRuntimeRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/runtime/") || r.Header.Get("X-Luna-Token") != "private-token" {
			t.Error("runtime scope lost")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	l := &lunaClient{base: server.URL, runtime: server.URL + "/api/runtime/test", token: "private-token", http: server.Client()}
	if err := l.request(context.Background(), "/provider/state", map[string]string{"state": "paused"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLunaRejectedOperationIsNotSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			json.NewEncoder(w).Encode(map[string]string{"id": "expired"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"selected": "radio", "last_result": map[string]any{"id": "expired", "success": false, "error": "provider command timed out"}})
	}))
	defer server.Close()
	l := &lunaClient{base: server.URL, id: lunaPluginID, http: server.Client()}
	if err := l.operation(context.Background(), "/api/media/command", map[string]string{"action": "next"}); err == nil {
		t.Fatal("failed operation acknowledged")
	}
	if l.selected.Load() {
		t.Fatal("failed handoff stole source")
	}
}

func TestLunaConfigurationRequiresCompleteLoopbackInvocation(t *testing.T) {
	for _, address := range []string{"http://example.com/api/runtime/test", "http://127.0.0.1:8099/other", "http://user@127.0.0.1:8099/api/runtime/test"} {
		t.Setenv("LUNA_API_URL", address)
		t.Setenv("LUNA_API_TOKEN", "token")
		if _, err := newLunaClient(); err == nil {
			t.Fatalf("bad runtime address accepted: %s", address)
		}
	}
}
