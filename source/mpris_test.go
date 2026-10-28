package main

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestMPRISControlsActualServerThroughIsolatedBus(t *testing.T) {
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon unavailable")
	}
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	bus := exec.Command(path, "--session", "--nofork", "--print-address=1")
	stdout, err := bus.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bus.Process.Kill(); bus.Wait() })
	address, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	address = strings.TrimSpace(address)
	input := &inputBuffer{}
	s := &Server{state: State{Status: "playing", Queue: []Track{{ID: "abcdefghijk", Title: "Test song", DurationMS: 123000}}, PositionMS: 1200}, player: &nativePlayer{in: input}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.mediaSession(ctx, address) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("MPRIS did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for !s.mediaConnected.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.mediaConnected.Load() {
		t.Fatal("MPRIS did not acquire a name")
	}
	client, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	object := client.Object(mediaName, mediaPath)
	for _, tc := range []struct{ method, status string }{{"Pause", "paused"}, {"Play", "playing"}, {"PlayPause", "paused"}} {
		if err := object.Call(mediaPlayerInterface+"."+tc.method, 0).Err; err != nil {
			t.Fatal(err)
		}
		if s.snapshot().Status != tc.status {
			t.Fatalf("%s failed to control the real server", tc.method)
		}
	}
	var properties map[string]dbus.Variant
	if err := object.Call("org.freedesktop.DBus.Properties.GetAll", 0, mediaPlayerInterface).Store(&properties); err != nil {
		t.Fatal(err)
	}
	if properties["PlaybackStatus"].Value() != "Paused" || properties["CanSeek"].Value() != false || properties["Position"].Value() != int64(1200000) {
		t.Fatal("MPRIS properties do not match player state")
	}
	if err := object.Call(mediaPlayerInterface+".Seek", 0, int64(1000)).Err; err == nil {
		t.Fatal("unsupported seeking was accepted")
	}
	if input.String() != "pause\nresume\npause\n" {
		t.Fatal("MPRIS did not send the expected native player commands")
	}
}
