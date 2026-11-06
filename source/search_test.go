package main

import (
	"context"
	"encoding/json"
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
	got, err := c.playLookup(context.Background(), "Yowai The Maid")
	if err != nil || len(got) != 1 || got[0].ID != "bcdefghijkl" || calls != 2 {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
	calls = 0
	got, err = c.playLookup(context.Background(), "Yabai The Maid")
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
	tracks, err := c.playLookup(context.Background(), "йовай зе мейд")
	if err != nil || len(tracks) != 1 || tracks[0].ID != "bcdefghijkl" || calls != 3 {
		t.Fatalf("%+v %v calls=%d", tracks, err, calls)
	}
}
