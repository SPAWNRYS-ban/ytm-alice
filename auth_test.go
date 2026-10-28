package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const accountFixture = `{"actions":[{"openPopupAction":{"popup":{"multiPageMenuRenderer":{"header":{"activeAccountHeaderRenderer":{"accountName":{"runs":[{"text":"Test Account"}]}}}}}}}]}`

func TestSessionSignatureAndHostBoundary(t *testing.T) {
	auth := Credentials{Cookie: "__Secure-3PAPISID=test-sid; SID=test-session", AuthUser: "1"}
	for _, target := range []string{"https://music.youtube.com/youtubei/v1/browse", "https://www.youtube.com/player", "https://example.googlevideo.com/audio", "http://music.youtube.com/browse", "https://music.youtube.com.evil.invalid/"} {
		req, _ := http.NewRequest("POST", target, nil)
		auth.apply(req, time.Unix(1700000000, 0))
		if req.URL.Host != "music.youtube.com" || req.URL.Scheme != "https" {
			if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
				t.Fatalf("credentials attached outside the Music HTTPS endpoint: %s", target)
			}
			continue
		}
		if req.Header.Get("Authorization") != "SAPISIDHASH 1700000000_9f729b4892ce284d6f5ea6bccaec61b1977815af" || req.Header.Get("X-Goog-AuthUser") != "1" {
			t.Fatal("incorrect browser-session signature")
		}
	}
}

func TestAuthImportIsTransactionalPersistentAndLogoutRemovesIt(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	c := newCatalog()
	valid := true
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/youtubei/v1/account/account_menu" || r.Header.Get("Cookie") == "" {
			t.Fatal("login was not checked with the authenticated account endpoint")
		}
		body := accountFixture
		if !valid {
			body = `{}` // A logged-out response can still have HTTP 200.
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	ctx := context.Background()
	old := &Credentials{Cookie: "__Secure-3PAPISID=first-session"}
	if err := c.replaceAuth(ctx, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(credentialPath())
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(credentialPath())
	if info.Mode().Perm() != 0600 {
		t.Fatal("session is accessible to other users")
	}
	reloaded := newCatalog()
	if err := reloaded.loadAuth(); err != nil || reloaded.credentials().Cookie != old.Cookie {
		t.Fatal("saved session did not survive reload")
	}
	valid = false
	if c.replaceAuth(ctx, &Credentials{Cookie: "SAPISID=rejected-session"}) == nil {
		t.Fatal("logged-out HTTP 200 accepted as authenticated")
	}
	after, _ := os.ReadFile(credentialPath())
	if !bytes.Equal(before, after) || c.credentials().Cookie != old.Cookie {
		t.Fatal("failed login destroyed the previous session")
	}
	s := &Server{catalog: c}
	for _, action := range []string{"status", "auth-status", "auth-check"} {
		raw, _ := json.Marshal(s.command(ctx, request{Action: action}))
		if bytes.Contains(raw, []byte("first-session")) || bytes.Contains(raw, []byte(`"cookie"`)) {
			t.Fatal("credential leaked into an API response")
		}
	}
	c.cache["old"] = catalogEntry{tracks: []Track{{ID: "private-track"}}}
	if err := c.logout(); err != nil || c.credentials() != nil || len(c.cache) != 0 {
		t.Fatal("logout retained in-memory session or personal cache")
	}
	if _, err := os.Stat(credentialPath()); !os.IsNotExist(err) {
		t.Fatal("logout retained session on disk")
	}
	if _, err := c.lookup(ctx, "мои лайки"); err == nil {
		t.Fatal("personal likes accessible without login")
	}
}

func TestSessionValidationRejectsHeaderInjection(t *testing.T) {
	for _, auth := range []Credentials{
		{Cookie: "SAPISID=x\r\nOther: header"},
		{Cookie: "SAPISID=x", AuthUser: "0\nOther: header"},
		{Cookie: "SAPISID=x", AuthUser: "-1"},
		{Cookie: "PREF=without-auth"},
	} {
		if auth.validate() == nil {
			t.Fatal("invalid credential input accepted")
		}
	}
	var missing *Credentials
	if missing.validate() == nil {
		t.Fatal("missing session accepted")
	}
}

func TestMusicRequestsDoNotForwardAuthOnRedirects(t *testing.T) {
	c := newCatalog()
	calls := 0
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Fatal("authenticated redirect was followed")
		}
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": {"https://accounts.google.com/"}}}, nil
	})}
	_, err := c.requestWithAuth(context.Background(), "browse", map[string]any{}, &Credentials{Cookie: "SAPISID=test-sid", AuthUser: "0"})
	if err == nil || calls != 1 {
		t.Fatal("redirect was not rejected")
	}
}

func TestPrivatePlaylistParsing(t *testing.T) {
	fixture := `{"items":[
        {"musicTwoRowItemRenderer":{"title":{"runs":[{"text":"My list"}]},"navigationEndpoint":{"browseEndpoint":{"browseId":"VLPL_private"}}}},
        {"musicTwoRowItemRenderer":{"title":{"runs":[{"text":"Duplicate"}]},"navigationEndpoint":{"browseEndpoint":{"browseId":"VLPL_private"}}}},
        {"musicTwoRowItemRenderer":{"title":{"simpleText":"Album"},"navigationEndpoint":{"browseEndpoint":{"browseId":"MPRE_an_album"}}}},
        {"musicTwoRowItemRenderer":{"title":{"simpleText":"Liked"},"navigationEndpoint":{"browseEndpoint":{"browseId":"VLLM"}}}}
    ]}`
	var data any
	if json.Unmarshal([]byte(fixture), &data) != nil {
		t.Fatal("bad fixture")
	}
	playlists := parsePlaylists(data)
	if len(playlists) != 2 || playlists[0].ID != "PL_private" || playlists[1].ID != "LM" {
		t.Fatalf("incorrect playlist extraction: %#v", playlists)
	}
}
