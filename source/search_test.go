package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestVoiceSearchDoesNotConfuseNearArtistNames(t *testing.T) {
	tracks := []Track{{ID: "abcdefghijk", Title: "hiraeth", Artists: "Yabai The Maid"}, {ID: "bcdefghijkl", Title: "Chasm", Artists: "Yowai The Maid"}}
	for _, query := range []string{"Yowai The Maid", "йовай зе мейд"} {
		got := relevantSongs(query, tracks)
		if len(got) != 1 || got[0].ID != "bcdefghijkl" {
			t.Fatalf("%s: %+v", query, got)
		}
	}
}
func TestVoiceSearchFallsBackToPublicVideoOnlyWhenNeeded(t *testing.T) {
	c := newCatalog()
	calls := 0
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("search exposed credentials")
		}
		if r.URL.Host == "music.youtube.com" {
			return jsonReply(map[string]any{"musicResponsiveListItemRenderer": map[string]any{"playlistItemData": map[string]any{"videoId": "abcdefghijk"}, "flexColumns": []any{map[string]any{"musicResponsiveListItemFlexColumnRenderer": map[string]any{"text": map[string]any{"runs": []any{map[string]any{"text": "Yabai The Maid - hiraeth"}}}}}}}}), nil
		}
		if r.URL.Host != "www.youtube.com" {
			t.Fatal(r.URL.Host)
		}
		return jsonReply(map[string]any{"videoRenderer": map[string]any{"videoId": "bcdefghijkl", "title": map[string]any{"simpleText": "Yowai The Maid - Chasm"}, "lengthText": map[string]any{"simpleText": "3:24"}, "ownerText": map[string]any{"simpleText": "Music uploads"}}}), nil
	})}
	got, err := c.playLookup(context.Background(), "Yowai The Maid", false)
	if err != nil || len(got) != 1 || got[0].ID != "bcdefghijkl" || calls != 2 {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
	calls = 0
	got, err = c.playLookup(context.Background(), "Yabai The Maid", false)
	if err != nil || len(got) != 1 || calls != 1 {
		t.Fatalf("matching Music song used fallback: %+v %v calls=%d", got, err, calls)
	}
}
func TestUnmatchedVoiceSearchPreservesCurrentPlayback(t *testing.T) {
	c := newCatalog()
	c.http = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return jsonReply(map[string]any{}), nil })}
	player := &nativePlayer{}
	s := &Server{catalog: c, player: player, generation: 7, state: State{Status: "playing", Queue: []Track{{ID: "abcdefghijk", Title: "Current"}}, PositionMS: 12000}}
	out := s.command(context.Background(), request{Action: "play", Query: "missing artist"})
	if out.Error == "" || s.player != player || s.generation != 7 || s.state.Status != "playing" || s.state.PositionMS != 12000 || s.state.Queue[0].ID != "abcdefghijk" {
		t.Fatal("failed search interrupted playback")
	}
}

func TestVoiceArtistSearchRejectsSongsOnlyMentioningTheArtist(t *testing.T) {
	tracks := []Track{{ID: "abcdefghijk", Title: "Мейдоведение - Yowai The Maid", Artists: "Trapishe"}}
	if len(relevantSongs("йовай зе мейд", tracks)) != 0 {
		t.Fatal("unrelated artist mention was treated as an artist match")
	}
	for _, query := range []string{"Мейдоведение", "Trapishe", "Trapishe Мейдоведение"} {
		if len(relevantSongs(query, tracks)) != 1 {
			t.Fatal("relevant title or artist rejected", query)
		}
	}
}

func TestCyrillicArtistSearchUsesLatinVideoQueryAndPrefersArtistPrefix(t *testing.T) {
	c := newCatalog()
	calls := 0
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "music.youtube.com" {
			return jsonReply(map[string]any{}), nil
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		title, id := "Мейдоведение - Yowai The Maid", "abcdefghijk"
		if body["query"] == "yovai ze maid" {
			title, id = "Yowai The Maid - Chasm", "bcdefghijkl"
		}
		return jsonReply(map[string]any{"videoRenderer": map[string]any{"videoId": id, "title": map[string]any{"simpleText": title}, "lengthText": map[string]any{"simpleText": "3:24"}}}), nil
	})}
	tracks, err := c.playLookup(context.Background(), "йовай зе мейд", false)
	if err != nil || len(tracks) != 1 || tracks[0].ID != "bcdefghijkl" || calls != 3 {
		t.Fatalf("%+v %v calls=%d", tracks, err, calls)
	}
}

func TestMusicOnlyVoiceSearchNeverSearchesVideos(t *testing.T) {
	c := newCatalog()
	hosts := []string{}
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		// Music has the song only under a longer title, so the strict prefix
		// match fails and a looser Music match must be used instead of video.
		return jsonReply(map[string]any{"musicResponsiveListItemRenderer": map[string]any{"playlistItemData": map[string]any{"videoId": "abcdefghijk"}, "flexColumns": []any{map[string]any{"musicResponsiveListItemFlexColumnRenderer": map[string]any{"text": map[string]any{"runs": []any{map[string]any{"text": "Get Lucky (feat. Pharrell Williams) - Daft Punk"}}}}}}}}), nil
	})}
	got, err := c.playLookup(context.Background(), "Daft Punk Get Lucky", true)
	if err != nil || len(got) != 1 || got[0].ID != "abcdefghijk" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, host := range hosts {
		if host != "music.youtube.com" {
			t.Fatalf("music-only search queried %s", host)
		}
	}
	hosts = nil
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		return jsonReply(map[string]any{}), nil
	})}
	if _, err := c.playLookup(context.Background(), "missing artist", true); err == nil {
		t.Fatal("unmatched music-only search must fail instead of playing a video")
	}
	for _, host := range hosts {
		if host != "music.youtube.com" {
			t.Fatalf("music-only search queried %s", host)
		}
	}
}

func TestMusicOnlyKeepsAudioTracksOfTVRadio(t *testing.T) {
	tile := func(id, title, kind string) map[string]any {
		return map[string]any{"tileRenderer": map[string]any{
			"contentType": "TILE_CONTENT_TYPE_VIDEO",
			"onSelectCommand": map[string]any{"watchEndpoint": map[string]any{"videoId": id, "playlistId": "RDAMVMm2zUrruKjDQ",
				"watchEndpointMusicSupportedConfigs": map[string]any{"watchEndpointMusicConfig": map[string]any{"musicVideoType": kind}}}},
			"metadata": map[string]any{"tileMetadataRenderer": map[string]any{"title": map[string]any{"simpleText": title}}},
		}}
	}
	root := map[string]any{"contents": []any{
		tile("hTWKbfoikeg", "Smells Like Teen Spirit", "MUSIC_VIDEO_TYPE_OMV"),
		tile("ljUtuoFt-8c", "Smells Like Teen Spirit", "MUSIC_VIDEO_TYPE_ATV"),
		tile("abcdefghijk", "Fan upload", "MUSIC_VIDEO_TYPE_UGC"),
		tile("bcdefghijkl", "No type", ""),
	}}
	all := parseTileQueue(root, "RDAMVMm2zUrruKjDQ")
	if len(all) != 4 || all[0].VideoType != "MUSIC_VIDEO_TYPE_OMV" || all[1].VideoType != "MUSIC_VIDEO_TYPE_ATV" {
		t.Fatalf("parsed %+v", all)
	}
	got := onlyMusic(all)
	if len(got) != 2 || got[0].ID != "ljUtuoFt-8c" || got[1].ID != "bcdefghijkl" {
		t.Fatalf("music only kept %+v, want the audio track and the untyped one", got)
	}
}

func TestMusicOnlyNeedsYouTubeOnly(t *testing.T) {
	for _, tc := range []struct{ youtube, music, active bool }{
		{false, false, false}, {false, true, false}, {true, false, false}, {true, true, true},
	} {
		if got := (Settings{YouTubeOnly: tc.youtube, MusicOnly: tc.music}).musicOnlyActive(); got != tc.active {
			t.Fatalf("youtube_only=%v music_only=%v: active=%v, want %v", tc.youtube, tc.music, got, tc.active)
		}
	}
}

func TestTitleLookupFindsSongCreditedToAnotherArtist(t *testing.T) {
	c := newCatalog()
	queries := []string{}
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		queries = append(queries, str(body["query"]))
		return jsonReply(map[string]any{"musicResponsiveListItemRenderer": map[string]any{"playlistItemData": map[string]any{"videoId": "abcdefghijk"}, "flexColumns": []any{map[string]any{"musicResponsiveListItemFlexColumnRenderer": map[string]any{"text": map[string]any{"runs": []any{map[string]any{"text": "Bochka, Bass, Kolbaser"}}}}}, map[string]any{"musicResponsiveListItemFlexColumnRenderer": map[string]any{"text": map[string]any{"runs": []any{map[string]any{"text": "XS Project", "navigationEndpoint": map[string]any{"browseEndpoint": map[string]any{"browseId": "UCxsproject"}}}}}}}}}}), nil
	})}
	if _, err := c.playLookup(context.Background(), "Паша Техник Bochka, Bass, Kolbaser", true); !errors.Is(err, errNoTracks) {
		t.Fatalf("artist mismatch should not match by artist and title: %v", err)
	}
	got, err := c.titleLookup(context.Background(), "Паша Техник", "Bochka, Bass, Kolbaser", true)
	if err != nil || len(got) != 1 || got[0].ID != "abcdefghijk" {
		t.Fatalf("%+v %v", got, err)
	}
	if queries[len(queries)-1] != "Bochka, Bass, Kolbaser" {
		t.Fatalf("title lookup searched %q", queries[len(queries)-1])
	}
}
