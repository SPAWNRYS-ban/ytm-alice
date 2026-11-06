package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTVLoginWaitsInTheBackgroundAndSavesTheSession(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	old := tvPollUnit
	tvPollUnit = time.Millisecond
	defer func() { tvPollUnit = old }()
	pages := 0
	script := tvScriptTransport(t, &pages)
	polls := make(chan struct{}, 10)
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/o/oauth2/device/code":
			return tvReply(200, `{"device_code":"dev-1","user_code":"ABC-DEF","verification_url":"https://www.google.com/device","expires_in":600,"interval":5}`), nil
		case "/o/oauth2/token":
			polls <- struct{}{}
			if len(polls) < 2 {
				return tvReply(428, `{"error":"authorization_pending"}`), nil
			}
			return tvReply(200, `{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600}`), nil
		case "/youtubei/v1/account/accounts_list":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				t.Fatal("account checked without the new token")
			}
			return jsonReply(map[string]any{"accountName": map[string]any{"simpleText": "TV Fixture"}}), nil
		}
		return script(r)
	})}
	s := &Server{catalog: c}
	out := s.authCommand(context.Background(), request{Action: "auth-tv-start"})
	if out.Error != "" || out.TVLogin == nil || out.TVLogin.Status != "waiting" || out.TVLogin.UserCode != "ABC-DEF" {
		t.Fatalf("start: %+v", out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		out = s.authCommand(context.Background(), request{Action: "auth-tv-status"})
		if out.TVLogin.Status != "waiting" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if out.TVLogin.Status != "done" || out.Auth == nil || !out.Auth.Valid || out.Auth.Account.Name != "TV Fixture" {
		t.Fatalf("status: %+v %+v", out.TVLogin, out.Auth)
	}
	if got := c.credentials(); !got.isTV() || got.TV.RefreshToken != "refresh-1" {
		t.Fatal("TV session not stored")
	}
	if strings.Contains(out.TVLogin.UserCode+out.TVLogin.Error, "dev-1") {
		t.Fatal("device code exposed")
	}
}

func TestTVLoginCancelStopsWaiting(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	old := tvPollUnit
	tvPollUnit = time.Millisecond
	defer func() { tvPollUnit = old }()
	pages := 0
	script := tvScriptTransport(t, &pages)
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/o/oauth2/device/code":
			return tvReply(200, `{"device_code":"dev-1","user_code":"ABC-DEF","verification_url":"https://www.google.com/device","expires_in":600,"interval":5}`), nil
		case "/o/oauth2/token":
			return tvReply(428, `{"error":"authorization_pending"}`), nil
		}
		return script(r)
	})}
	s := &Server{catalog: c}
	if out := s.authCommand(context.Background(), request{Action: "auth-tv-start"}); out.Error != "" {
		t.Fatal(out.Error)
	}
	s.authCommand(context.Background(), request{Action: "auth-tv-cancel"})
	time.Sleep(30 * time.Millisecond)
	if out := s.authCommand(context.Background(), request{Action: "auth-tv-status"}); out.TVLogin.Status != "idle" || c.credentials() != nil {
		t.Fatalf("cancelled login: %+v", out.TVLogin)
	}
}
