package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseCastMessages(t *testing.T) {
	in := `[[7,["c","SIDXYZ","",8]],[8,["S","gsess-1"]],[9,["loungeStatus",{"devices":"[{\"type\":\"REMOTE_CONTROL\",\"id\":\"a\"}]"}]],[10,["noop"]],[11,["setPlaylist",{"listId":"RDAMVM1","videoIds":"dQw4w9WgXcQ,kJQP7kiw5Fk","currentIndex":"1","videoId":"kJQP7kiw5Fk","currentTime":"0"}]]]`
	m := parseCastMessages(in)
	if len(m) != 5 {
		t.Fatalf("got %d messages: %+v", len(m), m)
	}
	c := &castReceiver{}
	c.noteMessages(m)
	if c.sid != "SIDXYZ" || c.gsid != "gsess-1" || c.aid != 11 {
		t.Fatalf("sid=%q gsid=%q aid=%d", c.sid, c.gsid, c.aid)
	}
	if pstr(m[4].Payload, "currentIndex") != "1" || pstr(m[4].Payload, "listId") != "RDAMVM1" {
		t.Fatalf("payload %+v", m[4].Payload)
	}
	if m[3].Name != "noop" || m[3].Payload != nil {
		t.Fatalf("noop %+v", m[3])
	}
}

func TestCastStatus(t *testing.T) {
	q := []Track{{ID: "dQw4w9WgXcQ"}}
	cases := map[string]int{"playing": 1, "paused": 2, "buffering": 3, "stopped": 4}
	for s, want := range cases {
		if got := castStatus(State{Status: s, Queue: q}); got != want {
			t.Errorf("%s: %d != %d", s, got, want)
		}
	}
	if castStatus(State{Status: "stopped"}) != -1 {
		t.Error("empty queue must be idle")
	}
}

func TestResolveRateLimitGate(t *testing.T) {
	c := newCatalog()
	c.limitedUntil = time.Now().Add(time.Minute)
	_, err := c.resolve(context.Background(), "dQw4w9WgXcQ")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("want rate-limit error without a request, got %v", err)
	}
}
