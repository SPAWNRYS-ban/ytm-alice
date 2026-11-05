package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const tvTestClientID = "123-fixture.apps.googleusercontent.com"

func tvReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func tvForm(t *testing.T, r *http.Request) url.Values {
	raw, _ := io.ReadAll(r.Body)
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return form
}

func tvScriptTransport(t *testing.T, pages *int) transportFunc {
	return func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "www.youtube.com" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected request %s", r.URL)
		}
		switch r.URL.Path {
		case "/tv":
			*pages++
			return tvReply(200, `<html><script id="base-js" src="/s/_/kabuki/_/js/m=base" nonce="x"></script></html>`), nil
		case "/s/_/kabuki/_/js/m=base":
			return tvReply(200, `var a={clientId:"`+tvTestClientID+`",Xb:"fixture-secret"};`), nil
		}
		t.Fatalf("unexpected path %s", r.URL.Path)
		return nil, nil
	}
}

func TestTVClientIsReadFromTheTVScriptAndCached(t *testing.T) {
	c := newCatalog()
	pages := 0
	c.http = &http.Client{Transport: tvScriptTransport(t, &pages)}
	for range 2 {
		client, err := c.tvClientCredentials(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if client.id != tvTestClientID || client.secret != "fixture-secret" {
			t.Fatalf("client not extracted: %+v", client)
		}
	}
	if pages != 1 {
		t.Fatalf("TV page downloaded %d times; the client must be cached", pages)
	}
}

func TestTVClientRejectsForeignScriptLocation(t *testing.T) {
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/tv" {
			t.Fatalf("followed a script outside youtube.com: %s", r.URL)
		}
		return tvReply(200, `<script id="base-js" src="https://evil.invalid/base.js"></script>`), nil
	})}
	if _, err := c.tvClientCredentials(context.Background()); err == nil {
		t.Fatal("a script from another host was accepted")
	}
}

func TestTVDeviceFlowPollsUntilConfirmed(t *testing.T) {
	c := newCatalog()
	polls := 0
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		form := tvForm(t, r)
		switch r.URL.Path {
		case "/o/oauth2/device/code":
			if form.Get("client_id") != tvTestClientID || form.Get("scope") != tvScope {
				t.Fatalf("device code request: %v", form)
			}
			return tvReply(200, `{"device_code":"dev-1","user_code":"ABC-DEF-GHI","verification_url":"https://evil.invalid/device","expires_in":1800,"interval":1}`), nil
		case "/o/oauth2/token":
			if form.Get("code") != "dev-1" || form.Get("client_secret") != "fixture-secret" {
				t.Fatalf("token request: %v", form)
			}
			polls++
			switch polls {
			case 1:
				return tvReply(428, `{"error":"authorization_pending"}`), nil
			case 2:
				return tvReply(403, `{"error":"slow_down"}`), nil
			}
			return tvReply(200, `{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600}`), nil
		}
		t.Fatalf("unexpected path %s", r.URL.Path)
		return nil, nil
	})}
	ctx := context.Background()
	client := tvClient{id: tvTestClientID, secret: "fixture-secret"}
	code, err := c.tvStartDevice(ctx, client, "device-id")
	if err != nil {
		t.Fatal(err)
	}
	if code.UserCode != "ABC-DEF-GHI" || code.VerificationURL != "https://www.google.com/device" || code.Interval != 5 {
		t.Fatalf("device code not normalized: %+v", code)
	}
	if _, slow, err := c.tvPollDevice(ctx, client, code.DeviceCode); !errors.Is(err, errTVPending) || slow {
		t.Fatalf("pending: %v %v", slow, err)
	}
	if _, slow, err := c.tvPollDevice(ctx, client, code.DeviceCode); !errors.Is(err, errTVPending) || !slow {
		t.Fatalf("slow_down: %v %v", slow, err)
	}
	token, _, err := c.tvPollDevice(ctx, client, code.DeviceCode)
	if err != nil || token.RefreshToken != "refresh-1" || token.ClientID != tvTestClientID {
		t.Fatalf("token: %+v %v", token, err)
	}
	// The access token from the device grant is reused without a refresh request.
	if access, err := c.tvAccessToken(ctx, token); err != nil || access != "access-1" {
		t.Fatalf("access: %q %v", access, err)
	}
	if polls != 3 {
		t.Fatalf("unexpected refresh request; polls=%d", polls)
	}
}

func TestTVAccessTokenRefreshAndRevocation(t *testing.T) {
	c := newCatalog()
	refreshes := 0
	revoked := false
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		form := tvForm(t, r)
		if r.URL.Path != "/o/oauth2/token" || form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "refresh-1" {
			t.Fatalf("refresh request: %s %v", r.URL.Path, form)
		}
		refreshes++
		if revoked {
			return tvReply(400, `{"error":"invalid_grant"}`), nil
		}
		return tvReply(200, `{"access_token":"access-2","expires_in":3600}`), nil
	})}
	ctx := context.Background()
	token := &TVToken{ClientID: tvTestClientID, ClientSecret: "fixture-secret", RefreshToken: "refresh-1"}
	for range 2 {
		if access, err := c.tvAccessToken(ctx, token); err != nil || access != "access-2" {
			t.Fatalf("access: %q %v", access, err)
		}
	}
	if refreshes != 1 {
		t.Fatalf("access token not cached: %d refreshes", refreshes)
	}
	c.forgetTVAccess()
	revoked = true
	if _, err := c.tvAccessToken(ctx, token); !errors.Is(err, errSessionExpired) || !strings.Contains(err.Error(), "YouTube TV") {
		t.Fatalf("revoked token must report an expired TV sign-in: %v", err)
	}
}

func TestTVCredentialsValidation(t *testing.T) {
	good := func() *Credentials {
		return &Credentials{TV: &TVToken{ClientID: tvTestClientID, ClientSecret: "fixture-secret", RefreshToken: "1//refresh-token"}}
	}
	if err := good().validate(); err != nil {
		t.Fatal(err)
	}
	hybrid := good()
	hybrid.Cookie, hybrid.AuthUser = "SAPISID=x", "2"
	if err := hybrid.validate(); err != nil {
		t.Fatalf("TV sign-in with cookies rejected: %v", err)
	}
	mixed := good()
	mixed.Cookie = "PREF=no-session"
	profile := good()
	profile.PageID = "123"
	foreign := good()
	foreign.TV.ClientID = "123-x.example.com"
	broken := good()
	broken.TV.RefreshToken = "token\r\nX-Injected: 1"
	for _, auth := range []*Credentials{mixed, profile, foreign, broken} {
		if auth.validate() == nil {
			t.Fatalf("invalid TV credentials accepted: %+v", auth.TV)
		}
	}
	req, _ := http.NewRequest("POST", "https://music.youtube.com/youtubei/v1/browse", nil)
	good().apply(req, time.Unix(1700000000, 0))
	if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
		t.Fatal("TV credentials were signed as a browser session")
	}
	hybrid.apply(req, time.Unix(1700000000, 0))
	if req.Header.Get("Cookie") != "SAPISID=x" || !strings.HasPrefix(req.Header.Get("Authorization"), "SAPISIDHASH ") || req.Header.Get("X-Goog-AuthUser") != "2" {
		t.Fatal("cookies of a TV sign-in were not signed for Music")
	}
}
