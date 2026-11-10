package main

import "testing"

func TestYouTubeAnywhereInPhrase(t *testing.T) {
	for _, tc := range []struct{ phrase, action, query string }{
		{"Алиса, включи трек звезда с ютуба", "play", "звезда"},
		{"включи трек ушёл с утра с ютуба", "play", "ушёл с утра"},
		{"включи song one из ютуба", "play", "song one"},
		{"найди на ютубе artist a", "play", "artist a"},
		{"поставь альбом album one с ютуба", "play", "альбом album one"},
		{"включи трек с ютуба", "recommendations", ""},
		{"включи музыку с ютуба", "recommendations", ""},
		{"включи ютуб", "recommendations", ""},
		{"включи на ютубе", "recommendations", ""},
		{"выключи ютуб", "stop", ""},
	} {
		a, q, ok := voiceAction(tc.phrase)
		if !ok || a != tc.action || q != tc.query {
			t.Fatalf("%q => %s %q %v, want %s %q", tc.phrase, a, q, ok, tc.action, tc.query)
		}
	}
	for _, phrase := range []string{"включи свет", "что такое ютуб", "включи мою волну", "включи музыку"} {
		if a, q, ok := voiceAction(phrase); ok {
			t.Fatalf("%q intercepted by phrase as %s %q", phrase, a, q)
		}
	}
}
