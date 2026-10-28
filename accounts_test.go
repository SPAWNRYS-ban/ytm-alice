package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const profilesFixture = `{"items":[
 {"accountItemRenderer":{"accountName":{"simpleText":"Personal"},"channelHandle":{"simpleText":"@personal"},"hasChannel":true,"isSelected":true,"serviceEndpoint":{"selectActiveIdentityEndpoint":{"supportedTokens":[{"accountSigninToken":{"signinUrl":"https://accounts.google.com/ServiceLogin?authuser=0&token=secret-token"}}]}}}},
 {"accountItemRenderer":{"accountName":{"simpleText":"Music channel"},"channelHandle":{"simpleText":"@music"},"hasChannel":true,"serviceEndpoint":{"selectActiveIdentityEndpoint":{"supportedTokens":[{"pageIdToken":{"pageId":"123456789"}},{"accountSessionToken":{"sessionIndex":0}},{"accountStateToken":{"opaque":"secret-token"}}]}}}},
 {"accountItemRenderer":{"accountName":{"simpleText":"Create channel"},"hasChannel":false,"serviceEndpoint":{"selectActiveIdentityEndpoint":{"supportedTokens":[{"pageIdToken":{"pageId":"111"}}]}}}},
 {"accountItemRenderer":{"accountName":{"simpleText":"Disabled"},"hasChannel":true,"isDisabled":true,"serviceEndpoint":{"selectActiveIdentityEndpoint":{"supportedTokens":[{"pageIdToken":{"pageId":"222"}}]}}}},
 {"accountItemRenderer":{"accountName":{"simpleText":"Injection"},"hasChannel":true,"serviceEndpoint":{"selectActiveIdentityEndpoint":{"supportedTokens":[{"pageIdToken":{"pageId":"123\nheader"}}]}}}}
]}`

func TestAccountDiscoveryAndSwitchKeepOnlyPublicProfileFields(t *testing.T) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		responseBody := profilesFixture
		switch r.URL.Path {
		case "/youtubei/v1/account/accounts_list":
			if get(body, "context", "client", "clientName") != "WEB" || body["requestType"] != "ACCOUNTS_LIST_REQUEST_TYPE_CHANNEL_SWITCHER" || r.Header.Get("X-Goog-PageId") != "" {
				t.Fatal("profile discovery used the wrong client or delegated identity")
			}
		case "/youtubei/v1/account/account_menu":
			responseBody = accountFixture
		case "/youtubei/v1/browse":
			if r.Header.Get("X-Goog-PageId") != "123456789" || get(body, "context", "user", "onBehalfOfUser") != "123456789" {
				t.Fatal("private library used the wrong YouTube profile")
			}
			responseBody = `{"items":[{"musicTwoRowItemRenderer":{"title":{"simpleText":"Channel playlist"},"navigationEndpoint":{"browseEndpoint":{"browseId":"VLPL_channel"}}}}]}`
		default:
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responseBody)), Header: make(http.Header)}, nil
	})}
	ctx := context.Background()
	if err := c.replaceAuth(ctx, &Credentials{Cookie: "SAPISID=private-session", AuthUser: "0"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{catalog: c}
	result := s.authCommand(ctx, request{Action: "auth-accounts"})
	if result.Error != "" || len(result.Accounts) != 2 || !result.Accounts[0].Selected || result.Accounts[1].PageID != "123456789" {
		t.Fatalf("wrong profiles: %#v", result.Accounts)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"private-session", "secret-token", "signinUrl", "supportedTokens"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("profile response leaked %s", secret)
		}
	}
	before, _ := os.ReadFile(credentialPath())
	if err := c.switchAccount(ctx, "0:999"); err == nil {
		t.Fatal("unavailable profile accepted")
	}
	after, _ := os.ReadFile(credentialPath())
	if string(before) != string(after) {
		t.Fatal("failed switch replaced saved session")
	}
	c.cache["personal"] = catalogEntry{tracks: []Track{{ID: "private-track"}}}
	if err := c.switchAccount(ctx, "0:123456789"); err != nil {
		t.Fatal(err)
	}
	if c.authStatus().Account.Name != "Music channel" || c.credentials().PageID != "123456789" || len(c.cache) != 0 {
		t.Fatal("profile switch did not update identity and clear private cache")
	}
	reloaded := newCatalog()
	if err := reloaded.loadAuth(); err != nil || reloaded.credentials().PageID != "123456789" {
		t.Fatal("profile selector was not persisted")
	}
	result = s.authCommand(ctx, request{Action: "auth-accounts"})
	if result.Accounts[0].Selected || !result.Accounts[1].Selected {
		t.Fatal("UI selection did not reflect the saved profile")
	}
	lists, err := c.library(ctx)
	if err != nil || len(lists) != 1 || lists[0].ID != "PL_channel" {
		t.Fatalf("delegated library failed: %v", err)
	}
}

func TestExpiredSessionCannotServeCachedLikesButPublicSearchStillWorks(t *testing.T) {
	c := newCatalog()
	c.auth = &Credentials{Cookie: "SAPISID=expired-session", AuthUser: "0"}
	c.cache["0:мои лайки"] = catalogEntry{tracks: []Track{{ID: "old-private-track"}}, until: time.Now().Add(time.Hour)}
	searchFixture, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		if strings.HasSuffix(r.URL.Path, "account_menu") {
			checks++
		} else if strings.HasSuffix(r.URL.Path, "search") {
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				t.Fatal("public search used expired credentials")
			}
			body = string(searchFixture)
		} else {
			t.Fatalf("expired session attempted private request %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	ctx := context.Background()
	for range 2 {
		if _, err := c.lookup(ctx, "мои лайки"); !errors.Is(err, errSessionExpired) {
			t.Fatalf("expired session served likes: %v", err)
		}
		if _, err := c.library(ctx); !errors.Is(err, errSessionExpired) {
			t.Fatalf("expired session served playlists: %v", err)
		}
	}
	if checks != 1 || !c.authStatus().Expired || c.authStatus().Valid {
		t.Fatal("expired status was not cached correctly")
	}
	if tracks, err := c.lookup(ctx, "public search"); err != nil || len(tracks) == 0 {
		t.Fatalf("expired session blocked public search: %v", err)
	}
}

func TestValidProfileWithEmptyLikesIsNotAnAuthenticationError(t *testing.T) {
	c := newCatalog()
	c.auth = &Credentials{Cookie: "SAPISID=valid-session", AuthUser: "0"}
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		if strings.HasSuffix(r.URL.Path, "account_menu") {
			body = accountFixture
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	s := &Server{catalog: c}
	result := s.command(context.Background(), request{Action: "liked"})
	if result.Error != "" || len(result.Tracks) != 0 || !c.authStatus().Valid {
		t.Fatal("empty likes were reported as a broken account")
	}
}
