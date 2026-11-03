package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

type countingTransport struct{ calls []string }

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls = append(t.calls, req.Method+" "+req.URL.Path)
	body := `<html>/s/player/abc/player_ias.vflset/en_US/base.js</html>`
	if req.URL.Path == "/youtubei/v1/player" {
		body = "{\n  \"playabilityStatus\": {\"status\": \"OK\"},\n  \"videoDetails\": {\"videoId\": \"dQw4w9WgXcQ\", \"title\": \"a};b\"}\n}"
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: req}, nil
}

func TestYTPageCacheServesRedundantPagesLocally(t *testing.T) {
	base := &countingTransport{}
	h := &http.Client{Transport: newYTPageCache(base)}
	req, _ := http.NewRequest(http.MethodPost, "https://www.youtube.com/youtubei/v1/player?key=", bytes.NewReader([]byte(`{"videoId":"dQw4w9WgXcQ"}`)))
	resp, err := h.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp, err = h.Get("https://www.youtube.com/watch?v=dQw4w9WgXcQ&bpctr=9999999999&has_verified=1")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	// Same pattern as github.com/kkdai/youtube/v2 video.go.
	m := regexp.MustCompile(`var ytInitialPlayerResponse\s*=\s*(\{.+?\});`).FindSubmatch(page)
	if m == nil {
		t.Fatalf("no player response in %s", page)
	}
	var pr map[string]any
	if err := json.Unmarshal(m[1], &pr); err != nil {
		t.Fatalf("player response does not parse: %v", err)
	}
	for i := 0; i < 2; i++ {
		resp, err := h.Get("https://youtube.com/embed/dQw4w9WgXcQ?hl=en")
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := io.ReadAll(resp.Body); !bytes.Contains(b, []byte("base.js")) {
			t.Fatalf("embed body %q", b)
		}
	}
	want := []string{"POST /youtubei/v1/player", "GET /embed/dQw4w9WgXcQ"}
	if strings.Join(base.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("network calls %v, want %v", base.calls, want)
	}
}
