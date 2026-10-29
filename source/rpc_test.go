package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestLargeSessionTravelsThroughSSHAndUnixControlAndSurvivesReload(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	catalog := newCatalog()
	catalog.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if len(r.Header.Get("Cookie")) <= 4096 {
			t.Error("large session was truncated")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(accountFixture)), Header: make(http.Header)}, nil
	})}
	server := &Server{catalog: catalog}
	// Include JSON-escaped characters to exercise the full supported envelope.
	cookie := "SAPISID=test-session; padding=" + strings.Repeat("<>&", 15000)
	raw, err := json.Marshal(request{Action: "auth-import", Auth: &Credentials{Cookie: cookie}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = rpcLoop(bytes.NewReader(append(raw, '\n')), &out, func(r request) (response, error) {
		client, daemon := net.Pipe()
		go server.controlConnection(context.Background(), daemon)
		defer client.Close()
		if err := json.NewEncoder(client).Encode(r); err != nil {
			return response{}, err
		}
		var result response
		err := json.NewDecoder(client).Decode(&result)
		return result, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var result response
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Error != "" || result.Auth == nil || !result.Auth.Valid {
		t.Fatalf("large login failed: error=%v response=%+v", err, result)
	}
	reloaded := newCatalog()
	if err := reloaded.loadAuth(); err != nil || reloaded.credentials() == nil || reloaded.credentials().Cookie != cookie {
		t.Fatalf("large persisted session failed to reload: %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte("test-session")) {
		t.Fatal("session leaked in the RPC response")
	}
}

func TestRPCRecoversAfterMalformedInputAndCommandFailure(t *testing.T) {
	in := "bad JSON\n{}\n{\"action\":\"status\"}\n{\"action\":\"stop\"}\n{\"action\":\"queue\"}\n"
	var out bytes.Buffer
	var actions []string
	err := rpcLoop(strings.NewReader(in), &out, func(r request) (response, error) {
		actions = append(actions, r.Action)
		if r.Action == "stop" {
			return response{}, errors.New("service disconnected")
		}
		return response{State: &State{Status: "paused"}}, nil
	})
	if err != nil || strings.Join(actions, ",") != "status,stop,queue" {
		t.Fatalf("request stream lost synchronization: %v %v", err, actions)
	}
	decoder := json.NewDecoder(&out)
	for i := 0; i < 5; i++ {
		var result response
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		wantError := i == 0 || i == 1 || i == 3
		if wantError != (result.Error != "") || (!wantError && result.State.Status != "paused") {
			t.Fatalf("unexpected response %d: %+v", i, result)
		}
	}
	if decoder.Decode(&response{}) != io.EOF {
		t.Fatal("unsolicited RPC response")
	}
}

func TestJumpUsesQueueAndRejectsInvalidIndexWithoutChangingIt(t *testing.T) {
	// Prevent player startup: this test exercises selection without media/network.
	s := &Server{closing: true, state: State{Queue: []Track{{ID: "first"}, {ID: "second"}}, Index: 0},
		latestSearch: []Track{{ID: "unrelated search"}}}
	ctx := context.Background()
	for _, index := range []int{0, -1, 3} {
		if result := s.command(ctx, request{Action: "jump", Index: index}); result.Error == "" {
			t.Fatalf("invalid queue position accepted: %d", index)
		}
		if s.snapshot().Index != 0 {
			t.Fatal("invalid selection changed current track")
		}
	}
	result := s.command(ctx, request{Action: "jump", Index: 2})
	if result.Error != "" || result.State.Index != 1 || len(result.State.Queue) != 2 || result.State.Queue[1].ID != "second" {
		t.Fatalf("queue replaced by search results: %+v", result)
	}
	if s.latestSearch[0].ID != "unrelated search" {
		t.Fatal("queue navigation changed search results")
	}
}

type inputBuffer struct {
	bytes.Buffer
	ack func(string)
}

func (b *inputBuffer) Write(data []byte) (int, error) {
	n, err := b.Buffer.Write(data)
	if b.ack != nil {
		go b.ack(string(data))
	}
	return n, err
}

func confirmedTestPlayer(s *Server) *inputBuffer {
	input := &inputBuffer{}
	s.player = &nativePlayer{in: input, confirmed: "playing"}
	input.ack = func(command string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.player == nil {
			return
		}
		if command == "pause\n" {
			s.player.confirmed = "paused"
			s.state.Status = "paused"
		}
		if command == "resume\n" {
			s.player.confirmed = "playing"
			s.state.Status = "playing"
		}
	}
	return input
}

func (b *inputBuffer) Close() error { return nil }

func TestToggleUsesActualPlayerState(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	s := &Server{state: State{Status: "playing"}}
	input := confirmedTestPlayer(s)
	for _, status := range []string{"paused", "playing", "paused"} {
		result := s.command(context.Background(), request{Action: "toggle"})
		if result.Error != "" || result.State.Status != status {
			t.Fatalf("toggle failed: %+v", result)
		}
	}
	if input.String() != "pause\nresume\npause\n" {
		t.Fatalf("wrong player commands: %q", input.String())
	}
}
