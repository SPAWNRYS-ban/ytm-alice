package main

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// An idle Alice sends nothing for minutes. The event subscription must stay
// one connection instead of being re-dialed after every quiet gap.
func TestVoiceLoopKeepsIdleConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			defer c.Close()
		}
	}()

	old := voiceIPCAddress
	voiceIPCAddress = func() string { return ln.Addr().String() }
	defer func() { voiceIPCAddress = old }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		(&Server{}).voiceLoop(ctx)
		close(done)
	}()

	time.Sleep(3 * time.Second) // longer than the old 2 s read deadline
	if n := accepted.Load(); n != 1 {
		t.Fatalf("connections to aliced while idle: %d, want 1", n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("voiceLoop did not stop after cancel")
	}
}
