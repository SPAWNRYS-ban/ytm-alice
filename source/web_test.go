package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func panelFixture() *webPanel {
	return newWebPanel(&Server{catalog: newCatalog(), state: State{Version: version, Status: "paused", Queue: []Track{{ID: "abcdefghijk", Title: "Test song"}}}})
}
func panelRequest(panel *webPanel, method, path, body, origin string, secure bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://alice.lan"+path, strings.NewReader(body))
	req.RemoteAddr = "192.168.1.42:50000"
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ytm-Client", "web")
	if secure {
		req.TLS = &tls.ConnectionState{}
	}
	out := httptest.NewRecorder()
	panel.ServeHTTP(out, req)
	return out
}
func TestWebLocalAccessNeedsNoCodeAndAssetsStayBounded(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	p := panelFixture()
	for _, path := range []string{"/", "/app.js", "/cookies.mjs", "/icons.mjs", "/icons.svg", "/logo.svg", "/style.css", "/placeholder.svg", "/api/info", "/api/state"} {
		out := panelRequest(p, "GET", path, "", "", false)
		if out.Code != 200 {
			t.Fatalf("%s: %d %s", path, out.Code, out.Body.String())
		}
		if out.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatal("missing frame protection")
		}
	}
	for _, path := range []string{"/youtube-session.json", "/web-key.pem", "/../auth.go", "/api/pair"} {
		if out := panelRequest(p, "GET", path, "", "", false); out.Code != 404 {
			t.Fatalf("unexpected exposed file: %s", path)
		}
	}
	if out := panelRequest(p, "POST", "/api/command", `{"action":"announce-off"}`, "http://alice.lan", false); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
}
func TestWebRejectsForeignSitesAndArbitraryCommands(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	p := panelFixture()
	for _, origin := range []string{"", "null", "http://evil.invalid", "https://alice.lan", "http://alice.lan:9999"} {
		if out := panelRequest(p, "POST", "/api/command", `{"action":"stop"}`, origin, false); out.Code != 403 {
			t.Fatalf("foreign origin accepted: %q", origin)
		}
	}
	for _, body := range []string{`{"action":"voice","query":"test"}`, `{"action":"daemon"}`, `{"action":"status","shell":"uname"}`, `{"action":"status","auth":{"cookie":"secret"}}`} {
		if out := panelRequest(p, "POST", "/api/command", body, "http://alice.lan", false); out.Code != 400 {
			t.Fatalf("unsafe command accepted: %s %d", body, out.Code)
		}
	}
	req := httptest.NewRequest("GET", "http://evil.invalid/api/state", nil)
	out := httptest.NewRecorder()
	p.ServeHTTP(out, req)
	if out.Code != 403 {
		t.Fatal("DNS rebinding host accepted")
	}
	req = httptest.NewRequest("POST", "http://alice.lan/api/command", strings.NewReader(`{"action":"stop"}`))
	req.Header.Set("Origin", "http://alice.lan")
	req.Header.Set("Content-Type", "application/json")
	out = httptest.NewRecorder()
	p.ServeHTTP(out, req)
	if out.Code != 403 {
		t.Fatal("simple cross-site form request accepted")
	}
}
func TestWebFollowsConnectionAddressChangesWithoutRestart(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	p := panelFixture()
	for _, host := range []string{"192.168.50.151", "192.168.50.152:80", "[fd42::1234]", "[::ffff:192.168.50.152]:80"} {
		ip := net.ParseIP(hostName(host))
		for _, method := range []string{"GET", "POST"} {
			path, body := "/api/state", ""
			if method == "POST" {
				path, body = "/api/command", `{"action":"announce-off"}`
			}
			req := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: ip, Port: 80}))
			req.Header.Set("Origin", "http://"+host)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Ytm-Client", "web")
			out := httptest.NewRecorder()
			p.ServeHTTP(out, req)
			if out.Code != http.StatusOK {
				t.Fatalf("current connection address rejected after network change: %s %s: %d", method, host, out.Code)
			}
		}
	}
}
func TestWebNewConnectionAddressStillRejectsUnrelatedHostsAndOrigins(t *testing.T) {
	p := panelFixture()
	for _, host := range []string{"evil.invalid", "198.51.100.234"} {
		req := httptest.NewRequest("GET", "http://"+host+"/api/state", nil)
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("192.168.50.152"), Port: 80}))
		out := httptest.NewRecorder()
		p.ServeHTTP(out, req)
		if out.Code != http.StatusForbidden {
			t.Fatalf("unrelated Host accepted: %s", host)
		}
	}
	req := httptest.NewRequest("POST", "http://192.168.50.152/api/command", strings.NewReader(`{"action":"stop"}`))
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("192.168.50.152"), Port: 80}))
	req.Header.Set("Origin", "http://evil.invalid")
	req.Header.Set("X-Ytm-Client", "web")
	out := httptest.NewRecorder()
	p.ServeHTTP(out, req)
	if out.Code != http.StatusForbidden {
		t.Fatal("foreign origin accepted after network change")
	}
}
func TestWebCookieImportAcceptsLANHTTPAndKeepsPreviousSessionOnFailure(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	p := panelFixture()
	p.server.catalog.auth = &Credentials{Cookie: "SAPISID=previous-session", AuthUser: "0"}
	p.server.catalog.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := accountFixture
		if strings.Contains(r.Header.Get("Cookie"), "invalid-session") {
			body = `{}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	body := `{"action":"auth-import","auth":{"cookie":"SAPISID=next-session","auth_user":"1"}}`
	out := panelRequest(p, "POST", "/api/command", body, "http://alice.lan", false)
	if out.Code != 200 || strings.Contains(out.Body.String(), "next-session") {
		t.Fatalf("LAN HTTP import failed or leaked credential: %d", out.Code)
	}
	before, _ := os.ReadFile(credentialPath())
	out = panelRequest(p, "POST", "/api/command", strings.ReplaceAll(body, "next-session", "invalid-session"), "http://alice.lan", false)
	after, _ := os.ReadFile(credentialPath())
	if out.Code != 422 || string(before) != string(after) {
		t.Fatal("failed import replaced previous credentials")
	}
}
func TestWebRequestLimitsAndHTTPNeedsNoCertificates(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "no-certificate-directory")
	t.Setenv("YTM_ALICE_DATA", directory)
	t.Setenv("YTM_ALICE_WEB_ADDR", "127.0.0.1:0")
	closePanel, err := panelFixture().server.startWeb()
	if err != nil {
		t.Fatal(err)
	}
	defer closePanel()
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("HTTP panel unexpectedly created certificate files")
	}
	body := `{"action":"search","query":"` + strings.Repeat("x", maxControlRequestBytes) + `"}`
	if out := panelRequest(panelFixture(), "POST", "/api/command", body, "http://alice.lan", false); out.Code != 400 {
		t.Fatal("oversized request accepted")
	}
}
func TestThumbnailsComeFromMetadataWithoutFetchingImages(t *testing.T) {
	var fixture any
	json.Unmarshal([]byte(`{"thumbnail":{"musicThumbnailRenderer":{"thumbnail":{"thumbnails":[{"url":"https://lh3.googleusercontent.com/small","width":120},{"url":"https://lh3.googleusercontent.com/cover","width":544},{"url":"https://evil.invalid/never","width":512}]}}}}`), &fixture)
	if thumbnailURL(fixture) != "https://lh3.googleusercontent.com/cover" {
		t.Fatal("incorrect artwork selection")
	}
	for _, url := range []string{"http://i.ytimg.com/a", "https://ytimg.com.evil.invalid/a", "https://user:pass@i.ytimg.com/a", "https://127.0.0.1/a"} {
		if safeThumbnail(url) != "" {
			t.Fatalf("unsafe thumbnail accepted: %s", url)
		}
	}
	p := panelFixture()
	state := p.server.snapshot()
	if state.Queue[0].ThumbnailURL != "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg" {
		t.Fatal("legacy queue lacks thumbnail fallback")
	}
	p.server.latestSearch = []Track{{ID: "abcdefghijk"}}
	out := p.server.command(context.Background(), request{Action: "select", Index: 1, Query: "differentID"})
	if out.Error == "" {
		t.Fatal("stale result selected a different song")
	}
}
