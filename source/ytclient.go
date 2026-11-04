package main

// The YouTube library makes several page requests per track besides the player
// API call: the youtube.com home page (visitor id), the watch page (only for the
// publish date) and the embed page plus base.js (player JS). With a fresh client
// per track nothing is cached, so every track switch costs up to five requests,
// four of them full HTML/JS pages. Shared IPs (CGNAT) hit 429 quickly that way.
// Keep one client so its own caches work, and answer the redundant pages locally.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	yt "github.com/kkdai/youtube/v2"
)

const (
	ytClientMaxAge = 6 * time.Hour
	embedCacheAge  = time.Hour
)

type ytPageCache struct {
	catalog *Catalog
	base    http.RoundTripper

	mu        sync.Mutex
	player    map[string][]byte // video id -> compact player response
	embed     []byte
	embedTime time.Time
}

func newYTPageCache(base http.RoundTripper) *ytPageCache {
	if base == nil {
		base = http.DefaultTransport
	}
	if ytNetLog {
		base = netLogTransport{base}
	}
	return &ytPageCache{base: base, player: map[string][]byte{}}
}

func isYouTubeHost(h string) bool { return h == "www.youtube.com" || h == "youtube.com" }

func syntheticResponse(req *http.Request, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

var ytNetLog = os.Getenv("YTM_ALICE_NETLOG") == "1"

// netLogTransport logs requests that actually leave the Station.
type netLogTransport struct{ base http.RoundTripper }

func (t netLogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	log.Printf("yt network: %s %s%s -> %d", req.Method, req.URL.Hostname(), req.URL.Path, status)
	return resp, err
}

func (p *ytPageCache) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || !isYouTubeHost(req.URL.Hostname()) {
		return p.network(req)
	}
	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/youtubei/v1/player":
		return p.playerCall(req)
	case req.Method == http.MethodGet && req.URL.Path == "/watch":
		// The library reads ytInitialPlayerResponse from the watch page; it is the
		// same document the player API has just returned.
		id := req.URL.Query().Get("v")
		p.mu.Lock()
		pr, ok := p.player[id]
		p.mu.Unlock()
		if ok {
			page := fmt.Sprintf("<html><script>var ytInitialPlayerResponse = %s;</script></html>", pr)
			return syntheticResponse(req, []byte(page)), nil
		}
	case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/embed/"):
		// Only the base.js path is taken from the embed page; it is the same for
		// every video until YouTube ships a new player.
		p.mu.Lock()
		cached := p.embed
		fresh := time.Since(p.embedTime) < embedCacheAge
		p.mu.Unlock()
		if cached != nil && fresh {
			return syntheticResponse(req, cached), nil
		}
		resp, err := p.network(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			return resp, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.embed, p.embedTime = body, time.Now()
		p.mu.Unlock()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	return p.network(req)
}

func (p *ytPageCache) playerCall(req *http.Request) (*http.Response, error) {
	var id string
	if req.GetBody != nil {
		if rc, err := req.GetBody(); err == nil {
			var in struct {
				VideoID string `json:"videoId"`
			}
			json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&in)
			rc.Close()
			id = in.VideoID
		}
	}
	resp, err := p.network(req)
	if err != nil || resp.StatusCode != http.StatusOK || !videoID.MatchString(id) {
		return resp, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if json.Compact(&compact, body) == nil {
		p.mu.Lock()
		if len(p.player) >= 8 {
			p.player = map[string][]byte{}
		}
		// The library cuts the JSON at the first "};". Semicolons occur in JSON
		// only inside strings, where \u003b is an equivalent escape.
		p.player[id] = bytes.ReplaceAll(compact.Bytes(), []byte(";"), []byte(`\u003b`))
		p.mu.Unlock()
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// withYT runs fn with the shared library client, creating it when missing or
// old. A failed call drops the client: the library may have switched it to a
// fallback player profile for that video.
func (c *Catalog) withYT(fn func(*yt.Client) error) error {
	c.ytMu.Lock()
	defer c.ytMu.Unlock()
	if c.ytClient == nil || time.Since(c.ytCreated) > ytClientMaxAge {
		h := *c.http
		pages := newYTPageCache(c.http.Transport)
		pages.catalog = c
		h.Transport = pages
		c.ytClient = &yt.Client{HTTPClient: &h, MaxRoutines: 1}
		c.ytCreated = time.Now()
	}
	err := fn(c.ytClient)
	var limited rateLimited
	if err != nil && !errors.As(err, &limited) {
		c.ytClient = nil
	}
	return err
}
