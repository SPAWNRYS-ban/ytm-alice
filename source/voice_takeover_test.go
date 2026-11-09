package main

import (
	"strings"
	"testing"
)

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

func TestSingleTrackKeepsBestMatchOnly(t *testing.T) {
	versions := []Track{{ID: "audio", Title: "Get Lucky"}, {ID: "clip", Title: "Get Lucky (Official Video)"}, {ID: "lyrics", Title: "Get Lucky (Lyrics)"}}
	if got := singleTrack(versions, true); len(got) != 1 || got[0].ID != "audio" {
		t.Fatalf("single request queue = %+v, want only the best match", got)
	}
	if got := singleTrack(versions, false); len(got) != 3 {
		t.Fatalf("artist/album request queue = %d tracks, want all 3", len(got))
	}
}

func TestTakeoverRequestCarriesTitleAndSpokenFallbacks(t *testing.T) {
	r := takeoverRequest(nativeMusic{Kind: "Track", Title: "Bochka, Bass, Kolbaser", Artist: "Паша Техник"}, "алиса включи колбасер")
	if r.Action != "play" || r.Query != "Паша Техник Bochka, Bass, Kolbaser" || !r.Single || r.Title != "Bochka, Bass, Kolbaser" || r.Artist != "Паша Техник" || r.Spoken != "колбасер" {
		t.Fatalf("track request = %+v", r)
	}
	r = takeoverRequest(nativeMusic{Kind: "Artist", Artist: "Кино"}, "алиса включи кино")
	if r.Single || r.Title != "" || r.Query != "Кино" {
		t.Fatalf("artist request = %+v", r)
	}
	if r = takeoverRequest(nativeMusic{Kind: "Radio", ID: "user:onyourwave"}, "алиса включи мою волну"); r.Action != "recommendations" || r.Spoken != "" {
		t.Fatalf("wave request = %+v", r)
	}
}

func TestSameTitleTrustsOnlyDistinctiveTitlesFromOtherArtists(t *testing.T) {
	// Live YouTube Music results for «Bochka, Bass, Kolbaser»; Yandex credits
	// the song to Паша Техник.
	kolbaser := []Track{
		{ID: "Lirc7fymdCA", Artists: "XS Project", Title: "Bochka, Bass, Kolbaser"},
		{ID: "MaUQNCyscFQ", Artists: "XS Project", Title: "Bochka Bass Kolbaser (Slowed)"},
		{ID: "qT1W9uXfep4", Artists: "3LEG", Title: "All Night Bochka"},
		{ID: "-6_u0SnjyrE", Artists: "HoNoTo", Title: "Bochka Bass Kolbaser"},
	}
	if got := sameTitle("Паша Техник", "Bochka, Bass, Kolbaser", kolbaser); len(got) != 2 || got[0].ID != "Lirc7fymdCA" {
		t.Fatalf("distinctive title: %+v", got)
	}
	lesnik := []Track{{ID: "abcdefghijk", Artists: "Другая группа", Title: "Лесник"}, {ID: "bcdefghijkl", Artists: "Король и Шут", Title: "Лесник"}}
	if got := sameTitle("Король и Шут", "Лесник", lesnik); len(got) != 1 || got[0].ID != "bcdefghijkl" {
		t.Fatalf("same artist must win: %+v", got)
	}
	if got := sameTitle("Король и Шут", "Лесник", lesnik[:1]); len(got) != 0 {
		t.Fatalf("a one-word title of another artist was accepted: %+v", got)
	}
}

func TestTakeoverLookupLogNamesRequestResultAndStep(t *testing.T) {
	r := request{Query: "Паша Техник Bochka, Bass, Kolbaser", Title: "Bochka, Bass, Kolbaser", Artist: "Паша Техник", Spoken: "колбасер"}
	got := takeoverLookupLog(r, "title", []Track{{Artists: "XS Project", Title: "Bochka, Bass, Kolbaser"}}, nil)
	want := `voice takeover: "Паша Техник — Bochka, Bass, Kolbaser" (said "колбасер") -> "XS Project — Bochka, Bass, Kolbaser" by title`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := takeoverLookupLog(request{Query: "кино", Spoken: "кино"}, "query", nil, errNoTracks); got != `voice takeover: "кино" (said "кино") not found` {
		t.Fatal(got)
	}
}

func TestCloudAnswerSeesNotFoundAndMusic(t *testing.T) {
	// Recorded on a Mini 2: «Включи madonna everything», Alice answered
	// «Не нашла ничего похожего» and kept listening.
	notFound := []byte(`{"force_server_request":false,"header":{"request_id":"r1"},"response":{"cards":[],"directives":[{"name":"tts_play_placeholder","payload":{"channel":"Dialog"},"type":"client_action"}],"is_streaming":true},"voice_response":{"has_voice_response":true,"should_listen":true}}`)
	if music, listen := cloudAnswer(notFound); music || !listen {
		t.Fatalf("not found: music=%v listen=%v", music, listen)
	}
	playing := []byte(`{"response":{"directives":[{"name":"audio_play","sub_name":"music"}]},"voice_response":{"should_listen":false}}`)
	if music, listen := cloudAnswer(playing); !music || listen {
		t.Fatalf("music: music=%v listen=%v", music, listen)
	}
	if music, listen := cloudAnswer(nil); music || listen {
		t.Fatal("empty response")
	}
}

func TestMusicRequestSkipsHomeAndBareCommands(t *testing.T) {
	for text, want := range map[string]string{
		"Алиса, включи madonna everything": "madonna everything",
		"поставь группу крови":             "группу крови",
		"включи свет":                      "",
		"включи таймер на 5 минут":         "",
		"включи музыку":                    "",
		"включи мою волну":                 "",
		"сколько времени":                  "",
	} {
		got, ok := musicRequest(text)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q %v, want %q", text, got, ok, want)
		}
	}
}

func TestSpokenFirstWhenCatalogPickedAnotherSong(t *testing.T) {
	for _, tc := range []struct {
		artist, title, said string
		first               bool
	}{
		// Recorded on a Mini 2: the catalog picked a different song.
		{"Umideep", "Madonna", "включи madonna fragile", true},
		{"Madonna", "Rain", "включи madonna трек everything", true},
		// The catalog song matches, or corrects misheard words.
		{"Паша Техник", "Bochka, Bass, Kolbaser", "включи колбасер", false},
		{"Кино", "Группа крови", "включи группа крови", false},
		{"Madonna", "Everything", "включи мадонна everything", false},
	} {
		r := takeoverRequest(nativeMusic{Kind: "Track", Artist: tc.artist, Title: tc.title}, tc.said)
		if r.SpokenFirst != tc.first {
			t.Errorf("%q vs %s — %s: spoken first = %v, want %v", tc.said, tc.artist, tc.title, r.SpokenFirst, tc.first)
		}
	}
	// A misheard foreign title does not match the catalog, so the spoken words
	// are tried first and the catalog song remains the fallback.
	if r := takeoverRequest(nativeMusic{Kind: "Track", Artist: "The Killers", Title: "Mr. Brightside"}, "включи мистер брайтсайд"); !r.SpokenFirst || r.Query != "The Killers Mr. Brightside" {
		t.Fatalf("misheard title: %+v", r)
	}
}

func TestSpokenQueryDropsFillerWordsInside(t *testing.T) {
	for text, want := range map[string]string{
		"включи madonna трек everything":         "madonna everything",
		"Алиса, включи madonna песня everything": "madonna everything",
		"включи трек numb":                       "numb",
		"включи песню":                           "",
	} {
		if got := spokenMusicQuery(text); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

func TestLogSafeDropsURLs(t *testing.T) {
	got := logSafe(`Post "https://music.youtube.com/youtubei/v1/search?key=SECRET&sig=abc": context deadline exceeded`)
	if strings.Contains(got, "SECRET") || strings.Contains(got, "youtube.com") || !strings.Contains(got, "context deadline exceeded") {
		t.Fatalf("unsafe or unreadable: %s", got)
	}
	if got := logSafe("альбом или плейлист не найден"); got != "альбом или плейлист не найден" {
		t.Fatal(got)
	}
}
