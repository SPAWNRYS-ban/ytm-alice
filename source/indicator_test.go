package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndicatorOwnsTriggerAcrossTrackChangesAndRestoresForAlice(t *testing.T) {
	root := t.TempDir()
	trigger := filepath.Join(root, "trigger")
	brightness := filepath.Join(root, "brightness")
	os.WriteFile(trigger, []byte("none [ledfb-ring]"), 0600)
	os.WriteFile(brightness, []byte("255"), 0600)
	led := playbackIndicator{paths: []string{brightness}}
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, state := range []string{"playing", "buffering", "playing", "searching", "playing"} {
		led.update(suppressPlaybackLED(false, state, true))
		if read(trigger) != "none" || read(brightness) != "0" {
			t.Fatalf("ring released during %s", state)
		}
	}
	led.update(suppressPlaybackLED(false, "playing", false))
	if read(trigger) != "ledfb-ring" {
		t.Fatal("Alice did not regain its trigger")
	}
	led.update(true)
	led.update(false)
	if read(trigger) != "ledfb-ring" {
		t.Fatal("shutdown did not restore trigger")
	}
}

func TestIndicatorRespectsSettingsPauseAndOtherOwners(t *testing.T) {
	for _, state := range []string{"paused", "stopped", "error"} {
		if suppressPlaybackLED(false, state, true) {
			t.Fatal(state)
		}
	}
	if suppressPlaybackLED(true, "playing", true) {
		t.Fatal("enabled lighting was suppressed")
	}
	root := t.TempDir()
	trigger, brightness := filepath.Join(root, "trigger"), filepath.Join(root, "brightness")
	os.WriteFile(trigger, []byte("[ledfb-ring] none"), 0600)
	os.WriteFile(brightness, []byte("255"), 0600)
	led := playbackIndicator{paths: []string{brightness}}
	led.update(true)
	os.WriteFile(trigger, []byte("[timer] none"), 0600)
	led.update(false)
	raw, _ := os.ReadFile(trigger)
	if string(raw) != "[timer] none" {
		t.Fatal("overwrote another owner")
	}
}

// Opt-in integration test for the emulator's real sysfs ring and trigger bridge.
func TestFirmwareIndicator(t *testing.T) {
	root := os.Getenv("YTM_TEST_LED_SYSFS")
	if root == "" {
		t.Skip("requires an isolated Station emulator")
	}
	paths := playbackLEDPaths(root)
	if len(paths) == 0 {
		t.Skip("model has no supported sysfs ring")
	}
	led := playbackIndicator{paths: paths}
	defer led.update(false)
	for _, status := range []string{"playing", "buffering", "playing"} {
		led.update(suppressPlaybackLED(false, status, true))
		for _, path := range paths {
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), "trigger"))
			if err != nil || activeLEDTrigger(raw) != "none" {
				t.Fatalf("%s: %s %v", status, raw, err)
			}
		}
	}
	led.update(false)
	if len(led.triggers) != 0 {
		t.Fatal("failed to restore firmware triggers")
	}
}
