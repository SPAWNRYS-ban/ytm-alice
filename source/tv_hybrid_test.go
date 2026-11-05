package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// hybridTransport serves the TV token, TV account, and the Music account menu,
// which answers as signed in only for the cookie named valid.
func hybridTransport(t *testing.T, calls *[]string) transportFunc {
	return func(r *http.Request) (*http.Response, error) {
		cookie, bearer := r.Header.Get("Cookie"), r.Header.Get("Authorization")
		*calls = append(*calls, r.URL.Host+r.URL.Path)
		switch {
		case r.URL.Path == "/o/oauth2/token":
			return tvReply(200, `{"access_token":"access-fixture","expires_in":3600}`), nil
		case r.URL.Host == "www.youtube.com":
			if bearer != "Bearer access-fixture" || cookie != "" {
				t.Fatalf("TV request %s: cookie=%q auth=%q", r.URL.Path, cookie, bearer)
			}
			if r.URL.Path == "/youtubei/v1/account/accounts_list" {
				return jsonReply(map[string]any{"accountName": map[string]any{"simpleText": "TV Fixture"}}), nil
			}
			return jsonReply(map[string]any{}), nil
		case r.URL.Host == "music.youtube.com":
			if strings.HasPrefix(bearer, "Bearer") {
				t.Fatal("TV token sent to Music")
			}
			if r.URL.Path == "/youtubei/v1/account/account_menu" {
				if cookie != "SAPISID=valid" || !strings.HasPrefix(bearer, "SAPISIDHASH ") {
					return jsonReply(map[string]any{}), nil
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(accountFixture)), Header: make(http.Header)}, nil
			}
			return jsonReply(map[string]any{"cookie": cookie}), nil
		}
		t.Fatalf("unexpected request %s", r.URL)
		return nil, nil
	}
}

func tvOnly() *Credentials {
	return &Credentials{TV: &TVToken{ClientID: tvTestClientID, ClientSecret: "fixture-secret", RefreshToken: "refresh-fixture"}}
}

func TestTVSignInUsesCookiesOnlyForMusicRequests(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	var calls []string
	c := newCatalog()
	c.http = &http.Client{Transport: hybridTransport(t, &calls)}
	c.auth = tvOnly()
	c.auth.Cookie, c.auth.AuthUser = "SAPISID=valid", "0"
	ctx := context.Background()
	if _, err := c.request(ctx, "browse", map[string]any{"browseId": "VLLM"}); err != nil {
		t.Fatal(err)
	}
	player, err := c.request(ctx, "player", map[string]any{"videoId": "aaaaaaaaaaa"})
	if err != nil || player["cookie"] != "SAPISID=valid" {
		t.Fatalf("player did not use the cookies: %v %v", player, err)
	}
	if calls[len(calls)-1] != "music.youtube.com/youtubei/v1/player" {
		t.Fatalf("calls: %v", calls)
	}
}

func TestCookiesAreAddedToATVSignIn(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	var calls []string
	c := newCatalog()
	c.http = &http.Client{Transport: hybridTransport(t, &calls)}
	ctx := context.Background()
	if err := c.replaceAuth(ctx, tvOnly()); err != nil {
		t.Fatal(err)
	}
	if err := c.importAuth(ctx, &Credentials{Cookie: "SAPISID=expired"}); err == nil || !strings.Contains(err.Error(), "18+") {
		t.Fatalf("expired cookies accepted: %v", err)
	}
	if got := c.credentials(); !got.isTV() || got.Cookie != "" {
		t.Fatal("a failed cookie import changed the TV sign-in")
	}
	if err := c.importAuth(ctx, &Credentials{Cookie: "SAPISID=valid"}); err != nil {
		t.Fatal(err)
	}
	got := c.credentials()
	if !got.isTV() || got.Cookie != "SAPISID=valid" {
		t.Fatalf("cookies did not join the TV sign-in: %+v", got)
	}
	if status := c.authStatus(); status.Method != "tv" || !status.Cookies || !status.Valid {
		t.Fatalf("status: %+v", status)
	}
	raw, _ := os.ReadFile(credentialPath())
	if !strings.Contains(string(raw), `"refresh_token":"refresh-fixture"`) || !strings.Contains(string(raw), `"cookie":"SAPISID=valid"`) {
		t.Fatalf("stored session: %s", raw)
	}
}

func TestTVLoginKeepsWorkingCookies(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	old := tvPollUnit
	tvPollUnit = 0
	defer func() { tvPollUnit = old }()
	var calls []string
	pages := 0
	script, hybrid := tvScriptTransport(t, &pages), hybridTransport(t, &calls)
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/tv", "/s/_/kabuki/_/js/m=base":
			return script(r)
		case "/o/oauth2/device/code":
			return tvReply(200, `{"device_code":"dev-1","user_code":"ABC-DEF","verification_url":"https://www.google.com/device","expires_in":600,"interval":5}`), nil
		case "/o/oauth2/token":
			if tvForm(t, r).Get("code") == "dev-1" {
				return tvReply(200, `{"access_token":"access-fixture","refresh_token":"refresh-fixture","expires_in":3600}`), nil
			}
		}
		return hybrid(r)
	})}
	ctx := context.Background()
	if err := c.replaceAuth(ctx, &Credentials{Cookie: "SAPISID=valid", AuthUser: "0"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{catalog: c}
	s.authCommand(ctx, request{Action: "auth-tv-start"})
	for deadline := time.Now().Add(5 * time.Second); s.catalog.tvLoginStatus().Status == "waiting" && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	got := c.credentials()
	if status := c.tvLoginStatus(); status.Status != "done" || !got.isTV() || got.Cookie != "SAPISID=valid" {
		t.Fatalf("TV login dropped the cookies: %+v %+v", status, got)
	}
}
