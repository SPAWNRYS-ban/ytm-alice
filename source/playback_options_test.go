package main

import "testing"

func TestRadioSkipsVideoVersionsOfTheSameSong(t *testing.T) {
	queue := []Track{{ID: "m2zUrruKjDQ", Artists: "The Killers", Title: "Mr. Brightside"}}
	radio := []Track{
		{ID: "gGdGFtwCNBE", Artists: "The Killers", Title: "Mr. Brightside (Official Music Video)"},
		{ID: "d8ekz_CSBVg", Artists: "Three Days Grace", Title: "I Hate Everything About You (Official Video)"},
		{ID: "ph9KP2_lT5Q", Artists: "Three Days Grace", Title: "I Hate Everything About You"},
		{ID: "hTWKbfoikeg", Artists: "Nirvana", Title: "Smells Like Teen Spirit"},
		{ID: "ljUtuoFt-8c", Artists: "Nirvana", Title: "Smells Like Teen Spirit"},
		{ID: "HyHNuVaZJ-k", Artists: "Gorillaz", Title: "Feel Good Inc."},
		{ID: "NxxjLD2pmlk", Artists: "Gorillaz и De La Soul", Title: "Feel Good Inc."},
		{ID: "pXRviuL6vMY", Artists: "twenty one pilots", Title: "Stressed Out"},
		{ID: "K1FlAphL2p8", Artists: "Twenty One Pilots", Title: "Stressed Out"},
		{ID: "8DyziWtkfBw", Artists: "Red Hot Chili Peppers", Title: "Can't Stop"},
		{ID: "rn_YodiJO6k", Artists: "Red Hot Chili Peppers", Title: "Otherside"},
	}
	got := newRadioTracks(queue, radio)
	want := []string{"ph9KP2_lT5Q", "hTWKbfoikeg", "HyHNuVaZJ-k", "pXRviuL6vMY", "8DyziWtkfBw", "rn_YodiJO6k"}
	if len(got) != len(want) {
		t.Fatalf("got %d tracks %+v, want %v", len(got), got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("track %d = %s (%s), want %s", i, got[i].ID, got[i].Title, id)
		}
	}
}
