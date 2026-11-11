package main

import (
	"strings"
	"testing"
)

func TestRadioSkipsVideoVersionsOfTheSameSong(t *testing.T) {
	queue := []Track{{ID: "a-audio", Artists: "Artist A", Title: "Song One"}}
	radio := []Track{
		{ID: "a-clip", Artists: "Artist A", Title: "Song One (Official Music Video)"},
		{ID: "b-clip", Artists: "Artist B", Title: "Song Two (Official Video)"},
		{ID: "b-audio", Artists: "Artist B", Title: "Song Two"},
		{ID: "c-first", Artists: "Artist C", Title: "Song Three"},
		{ID: "c-second", Artists: "Artist C", Title: "Song Three"},
		{ID: "d-solo", Artists: "Artist D", Title: "Song Four"},
		{ID: "d-guest", Artists: "Artist D и Guest", Title: "Song Four"},
		{ID: "e-lower", Artists: "artist e", Title: "Song Five"},
		{ID: "e-upper", Artists: "Artist E", Title: "Song Five"},
		{ID: "f-one", Artists: "Artist F", Title: "Song Six"},
		{ID: "f-two", Artists: "Artist F", Title: "Song Seven"},
	}
	got := newRadioTracks(queue, radio)
	want := []string{"b-audio", "c-first", "d-solo", "e-lower", "f-one", "f-two"}
	if len(got) != len(want) {
		t.Fatalf("got %d tracks %+v, want %v", len(got), got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("track %d = %s (%s), want %s", i, got[i].ID, got[i].Title, id)
		}
	}
}

func TestAlbumTracksTakeTheHeaderArtist(t *testing.T) {
	// Shape of a YouTube Music album page: the header names the artist, the
	// track rows do not.
	page := map[string]any{"contents": map[string]any{"twoColumnBrowseResultsRenderer": map[string]any{
		"tabs": []any{map[string]any{"tabRenderer": map[string]any{"content": map[string]any{"sectionListRenderer": map[string]any{"contents": []any{
			map[string]any{"musicResponsiveHeaderRenderer": map[string]any{
				"title":            map[string]any{"runs": []any{map[string]any{"text": "Album One"}}},
				"straplineTextOne": map[string]any{"runs": []any{map[string]any{"text": "Artist A"}}},
			}},
		}}}}}},
	}}}
	if got := albumArtist(page); got != "Artist A" {
		t.Fatalf("album artist = %q", got)
	}
	tracks := withArtist([]Track{{ID: "a", Title: "Song One"}, {ID: "b", Title: "Duet", Artists: "Artist A, Guest"}}, "Artist A")
	if tracks[0].Artists != "Artist A" || tracks[1].Artists != "Artist A, Guest" {
		t.Fatalf("%+v", tracks)
	}
}

func TestYouTubeOnlyLeavesMusicToAlicesAnswer(t *testing.T) {
	s := &Server{settings: &Settings{YouTubeOnly: true, VoiceCommands: map[string]string{"моя подборка": "play"}}}
	for _, text := range []string{"поставь song one", "включи песню song two", "включи альбом album one"} {
		if a, q, ok := s.voiceAction(text); ok {
			t.Errorf("%q kept by the text route: %s %q", text, a, q)
		}
	}
	for text, want := range map[string]string{"включи на ютубе song one": "play", "дальше": "next", "включи мои лайки": "play", "что играет": "what"} {
		if a, _, ok := s.voiceAction(text); !ok || a != want {
			t.Errorf("%q: %s %v, want %s", text, a, ok, want)
		}
	}
	off := &Server{settings: &Settings{}}
	if a, q, ok := off.voiceAction("поставь song one"); !ok || a != "play" || q != "song one" {
		t.Fatalf("without youtube_only the upstream route stays: %s %q %v", a, q, ok)
	}
}

func TestRecommendationsSkipVideoVersionsOfTheSameSong(t *testing.T) {
	mix := []Track{
		{ID: "a1", Artists: "Artist A и Artist B", Title: "Song One"},
		{ID: "a2", Artists: "Artist A и Artist B", Title: "Song One"},
		{ID: "b1", Artists: "Artist C", Title: "Song Two"},
		{ID: "b2", Artists: "Artist C и Artist D", Title: "Song Two"},
		{ID: "c1", Artists: "Artist E и Artist F", Title: "Song Three (remix)"},
		{ID: "c2", Artists: "Artist E", Title: "Song Three"},
	}
	got := []string{}
	for _, track := range newRadioTracks(nil, mix) {
		got = append(got, track.ID)
	}
	if strings.Join(got, " ") != "a1 b1 c1 c2" {
		t.Fatalf("got %v", got)
	}
}
