package main

import "testing"

func TestYouTubeAnywhereInPhrase(t *testing.T) {
	for _, tc := range []struct{ phrase, action, query string }{
		{"Алиса, включи трек панцушот с ютуба", "play", "панцушот"},
		{"включи трек он сошел с ума с ютуба", "play", "он сошел с ума"},
		{"включи numb из ютуба", "play", "numb"},
		{"найди на ютубе daft punk", "play", "daft punk"},
		{"поставь альбом meteora с ютуба", "play", "альбом meteora"},
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
