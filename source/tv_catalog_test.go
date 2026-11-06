package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

func tvTile(id, playlist, title, artist, duration string) map[string]any {
	watch := map[string]any{"videoId": id}
	if playlist != "" {
		watch["playlistId"] = playlist
	}
	return map[string]any{"tileRenderer": map[string]any{
		"contentType": "TILE_CONTENT_TYPE_VIDEO",
		"header": map[string]any{"tileHeaderRenderer": map[string]any{
			"thumbnail":         map[string]any{"thumbnails": []any{map[string]any{"url": "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg", "width": 480.0}}},
			"thumbnailOverlays": []any{map[string]any{"thumbnailOverlayTimeStatusRenderer": map[string]any{"text": map[string]any{"simpleText": duration}}}},
		}},
		"metadata": map[string]any{"tileMetadataRenderer": map[string]any{
			"title": map[string]any{"simpleText": title},
			"lines": []any{map[string]any{"lineRenderer": map[string]any{"items": []any{map[string]any{"lineItemRenderer": map[string]any{"text": map[string]any{"simpleText": artist}}}}}}},
		}},
		"onSelectCommand":    map[string]any{"watchEndpoint": watch},
		"onLongPressCommand": map[string]any{"showMenuCommand": map[string]any{"menu": map[string]any{"menuRenderer": map[string]any{"items": []any{}}}}},
	}}
}

func tvPlaylistTile(id, title string) map[string]any {
	return map[string]any{"tileRenderer": map[string]any{
		"contentType":     "TILE_CONTENT_TYPE_PLAYLIST",
		"metadata":        map[string]any{"tileMetadataRenderer": map[string]any{"title": map[string]any{"simpleText": title}}},
		"onSelectCommand": map[string]any{"watchEndpoint": map[string]any{"videoId": "dQw4w9WgXcQ", "playlistId": id}},
	}}
}

func tvPlaylistPage(tiles []any, next string) map[string]any {
	list := map[string]any{"contents": tiles, "playlistId": "LM"}
	if next != "" {
		list["continuations"] = []any{map[string]any{"nextContinuationData": map[string]any{"continuation": next}}}
	}
	return map[string]any{"contents": map[string]any{"tvBrowseRenderer": map[string]any{"content": map[string]any{"tvSurfaceContentRenderer": map[string]any{"content": map[string]any{"twoColumnRenderer": map[string]any{"rightColumn": map[string]any{"playlistVideoListRenderer": list}}}}}}}}
}

type tvFake struct {
	t       *testing.T
	revoked bool
	calls   []string
	reply   func(endpoint string, body map[string]any) any
}

func (f *tvFake) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/o/oauth2/token" {
		if f.revoked {
			return tvReply(400, `{"error":"invalid_grant"}`), nil
		}
		return tvReply(200, `{"access_token":"access-fixture","expires_in":3600}`), nil
	}
	var body map[string]any
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}
	endpoint := strings.TrimPrefix(r.URL.Path, "/youtubei/v1/")
	f.calls = append(f.calls, r.URL.Host+" "+endpoint)
	if r.Header.Get("Cookie") != "" {
		f.t.Fatal("cookie header sent with a TV sign-in")
	}
	if r.URL.Host == "www.youtube.com" {
		if r.Header.Get("Authorization") != "Bearer access-fixture" || get(body, "context", "client", "clientName") != "TVHTML5" || body["isAudioOnly"] != nil {
			f.t.Fatalf("TV request is not a TVHTML5 Bearer request: %v %v", r.Header, body)
		}
	} else if r.Header.Get("Authorization") != "" {
		f.t.Fatalf("TV token leaked to %s", r.URL.Host)
	}
	if endpoint == "account/accounts_list" {
		return jsonReply(map[string]any{"contents": []any{map[string]any{"accountItem": map[string]any{"accountName": map[string]any{"simpleText": "TV Fixture"}}}}}), nil
	}
	return jsonReply(f.reply(endpoint, body)), nil
}

func tvCatalog(t *testing.T, reply func(string, map[string]any) any) (*Catalog, *tvFake) {
	t.Setenv("YTM_ALICE_DATA", t.TempDir())
	fake := &tvFake{t: t, reply: reply}
	c := newCatalog()
	c.http = &http.Client{Transport: fake}
	c.auth = &Credentials{AuthUser: "0", TV: &TVToken{ClientID: tvTestClientID, ClientSecret: "fixture-secret", RefreshToken: "refresh-fixture"}}
	return c, fake
}

func TestTVLikedTracksFollowPlaylistPages(t *testing.T) {
	c, fake := tvCatalog(t, func(endpoint string, body map[string]any) any {
		if endpoint != "browse" {
			t.Fatalf("unexpected endpoint %s", endpoint)
		}
		if body["browseId"] == "VLLM" {
			return tvPlaylistPage([]any{tvTile("aaaaaaaaaaa", "LM", "First", "Artist A", "3:05"), tvTile("bbbbbbbbbbb", "LM", "Second", "Artist B", "1:02:03")}, "page-2")
		}
		if body["continuation"] == "page-2" {
			return map[string]any{"continuationContents": map[string]any{"playlistVideoListContinuation": map[string]any{"contents": []any{tvTile("ccccccccccc", "LM", "Third", "Artist C", "4:00")}}}}
		}
		t.Fatalf("unexpected browse %v", body)
		return nil
	})
	tracks, err := c.lookup(context.Background(), "мои лайки")
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 3 || tracks[0].Title != "First" || tracks[0].Artists != "Artist A" || tracks[0].DurationMS != 185000 || tracks[1].DurationMS != 3723000 || tracks[2].ID != "ccccccccccc" {
		t.Fatalf("liked tracks: %+v", tracks)
	}
	if status := c.authStatus(); !status.Valid || status.Account == nil || status.Account.Name != "TV Fixture" {
		t.Fatalf("TV account not checked: %+v", status)
	}
	if strings.Join(fake.calls, ",") != "www.youtube.com account/accounts_list,www.youtube.com browse,www.youtube.com browse" {
		t.Fatalf("calls: %v", fake.calls)
	}
}

func TestTVRadioKeepsOnlyTheQueueAndSearchIsAnonymous(t *testing.T) {
	c, fake := tvCatalog(t, func(endpoint string, body map[string]any) any {
		switch endpoint {
		case "next":
			queue := []any{tvTile("aaaaaaaaaaa", "RDAMVMaaaaaaaaaaa", "Seed", "A", "3:00"), tvTile("bbbbbbbbbbb", "RDAMVMaaaaaaaaaaa", "Next", "B", "3:00")}
			other := []any{tvTile("zzzzzzzzzzz", "RDzzzzzzzzzzz", "Unrelated", "Z", "3:00")}
			shelf := func(items []any) any {
				return map[string]any{"shelfRenderer": map[string]any{"content": map[string]any{"horizontalListRenderer": map[string]any{"items": items}}}}
			}
			return map[string]any{"contents": map[string]any{"singleColumnWatchNextResults": map[string]any{"pivot": map[string]any{"sectionListRenderer": map[string]any{"contents": []any{shelf(queue), shelf(other)}}}}}}
		case "search":
			return map[string]any{"contents": []any{map[string]any{"musicResponsiveListItemRenderer": map[string]any{"playlistItemData": map[string]any{"videoId": "sssssssssss"}, "flexColumns": []any{map[string]any{"musicResponsiveListItemFlexColumnRenderer": map[string]any{"text": map[string]any{"simpleText": "Found"}}}}}}}}
		}
		t.Fatalf("unexpected endpoint %s", endpoint)
		return nil
	})
	radio := c.radio(context.Background(), "aaaaaaaaaaa")
	if len(radio) != 2 || radio[1].ID != "bbbbbbbbbbb" {
		t.Fatalf("radio: %+v", radio)
	}
	found, err := c.lookup(context.Background(), "песня")
	if err != nil || len(found) != 1 || found[0].ID != "sssssssssss" {
		t.Fatalf("search: %+v %v", found, err)
	}
	if fake.calls[1] != "music.youtube.com search" {
		t.Fatalf("search did not stay on the public Music endpoint: %v", fake.calls)
	}
}

func TestTVLibraryListsPlaylistsButNotMixes(t *testing.T) {
	c, _ := tvCatalog(t, func(endpoint string, body map[string]any) any {
		if body["browseId"] != "FElibrary" {
			t.Fatalf("unexpected request %s %v", endpoint, body)
		}
		return map[string]any{"contents": []any{tvPlaylistTile("PLfixture123", "Road"), tvPlaylistTile("RDAMVMaaaaaaaaaaa", "Mix"), tvTile("aaaaaaaaaaa", "", "Video", "A", "1:00")}}
	})
	lists, err := c.library(context.Background())
	if err != nil || len(lists) != 1 || lists[0].ID != "PLfixture123" || lists[0].Title != "Road" {
		t.Fatalf("library: %+v %v", lists, err)
	}
}

func TestTVRevokedTokenMarksTheSessionExpired(t *testing.T) {
	c, fake := tvCatalog(t, func(string, map[string]any) any { return map[string]any{} })
	fake.revoked = true
	_, err := c.lookup(context.Background(), "мои лайки")
	if !errors.Is(err, errSessionExpired) || !strings.Contains(err.Error(), "YouTube TV") {
		t.Fatalf("revoked token: %v", err)
	}
	if status := c.authStatus(); !status.Expired {
		t.Fatalf("status not expired: %+v", status)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("requests sent without a valid token: %v", fake.calls)
	}
}

func TestTVSignInIsSavedAndLoaded(t *testing.T) {
	c, _ := tvCatalog(t, func(string, map[string]any) any { return map[string]any{} })
	auth := c.auth
	c.auth = nil
	if err := c.replaceAuth(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(credentialPath())
	if !strings.Contains(string(raw), `"refresh_token":"refresh-fixture"`) || strings.Contains(string(raw), "access-fixture") {
		t.Fatalf("stored session: %s", raw)
	}
	loaded := newCatalog()
	if err := loaded.loadAuth(); err != nil {
		t.Fatal(err)
	}
	if got := loaded.credentials(); !got.isTV() || got.TV.RefreshToken != "refresh-fixture" {
		t.Fatalf("loaded: %+v", got)
	}
	if err := c.switchAccount(context.Background(), "1:"); err == nil {
		t.Fatal("profile switch accepted for a TV sign-in")
	}
}
