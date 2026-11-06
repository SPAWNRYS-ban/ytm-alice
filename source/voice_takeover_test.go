package main

import "testing"

// Trimmed AliceResponse JSON in the shape captured on a Mini 2 (quasar 3.21).
const (
	nativeWave = `{"header":{"request_id":"r1"},"response":{"cards":[{"text":"Включаю."}],"directives":[
		{"name":"tts_play_placeholder","sub_name":"tts_play_placeholder","type":"client_action","payload":{"channel":"dialog"}},
		{"name":"audio_play","sub_name":"music","type":"client_action","payload":{"metadata":{"title":"Bangaranga","subtitle":"DARA",
			"glagol_metadata":{"music_metadata":{"id":"user:onyourwave","type":"Radio"}}},"stream":{"id":"152653243"}}}]}}`
	nativeArtist = `{"response":{"directives":[{"name":"audio_play","sub_name":"music","type":"client_action","payload":{"metadata":{"title":"Numb","subtitle":"Linkin Park",
		"glagol_metadata":{"music_metadata":{"id":"36800","type":"Artist"}}}}}]}}`
	nativeLight = `{"response":{"cards":[{"text":"Где мне это сделать?"}],"directives":[{"name":"tts_play_placeholder","sub_name":"tts_play_placeholder","type":"client_action","payload":{}}]}}`
)

func busyWithResponse(text, requestID, response string) []byte {
	asr := cat(pb(1, []byte(text)), pi(2, 1), pi(4, 1))
	answer := cat(pb(1, []byte("Включаю.")), pb(5, []byte(response)))
	body := cat(pi(1, 3), pb(2, []byte(text)), pb(3, answer), pb(7, []byte(requestID)), pb(15, asr))
	return cat(pb(252, body), pi(253, 3))
}

func TestBusyEventCarriesCloudResponse(t *testing.T) {
	event := decodeAlice(busyWithResponse("Включи музыку", "r1", nativeWave))
	if event.State != 3 || event.RequestID != "r1" || string(event.Response) != nativeWave {
		t.Fatalf("cloud response lost: %+v", event)
	}
	if len(decodeAlice(aliceStateFrame(3, "Включи музыку", "r1", true)).Response) != 0 {
		t.Fatal("response invented before the cloud answered")
	}
}

func TestNativeMusicIsReplayedOnYouTube(t *testing.T) {
	for _, tc := range []struct{ response, text, action, query string }{
		{nativeWave, "Включи музыку", "recommendations", ""},
		{nativeWave, "Включи мою волну", "recommendations", ""},
		{nativeArtist, "Включи linkin park", "play", "Linkin Park"},
		{`{"response":{"directives":[{"name":"audio_play","sub_name":"music","payload":{"metadata":{"title":"Numb","subtitle":"Linkin Park","glagol_metadata":{"music_metadata":{"type":"Track"}}}}}]}}`, "включи намб", "play", "Linkin Park Numb"},
		{`{"response":{"directives":[{"name":"audio_play","sub_name":"music","payload":{"metadata":{"glagol_metadata":{"music_metadata":{"id":"genre:rock","type":"Radio"}}}}}]}}`, "Алиса, поставь рок", "play", "рок"},
		{`{"response":{"directives":[{"name":"audio_play","sub_name":"music","payload":{"metadata":{"glagol_metadata":{"music_metadata":{"type":"Generative"}}}}}]}}`, "включи что-нибудь", "recommendations", ""},
	} {
		m, ok := parseNativeMusic([]byte(tc.response))
		if !ok {
			t.Fatalf("%q: native music not detected", tc.text)
		}
		if a, q := takeoverAction(m, tc.text); a != tc.action || q != tc.query {
			t.Fatalf("%q => %s %q, want %s %q", tc.text, a, q, tc.action, tc.query)
		}
	}
	for _, response := range []string{nativeLight, "", "{", `{"response":{"directives":[{"name":"audio_play","sub_name":"podcast"}]}}`} {
		if _, ok := parseNativeMusic([]byte(response)); ok {
			t.Fatalf("non-music answer taken over: %s", response)
		}
	}
}

func TestYouTubeOnlySetting(t *testing.T) {
	if defaultSettings().YouTubeOnly {
		t.Fatal("native music must stay with Alice unless enabled")
	}
	r, err := configRequest([]string{"set", "youtube_only", "true"})
	if err != nil || r.Settings == nil || r.Settings.YouTubeOnly == nil || !*r.Settings.YouTubeOnly {
		t.Fatalf("youtube_only not settable: %+v %v", r.Settings, err)
	}
}
