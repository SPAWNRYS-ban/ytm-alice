package main

import (
	"strings"
	"testing"
)

// Trimmed AliceResponse JSON in the shape captured on a Mini 2 (quasar 3.21).
const (
	nativeWave = `{"header":{"request_id":"r1"},"response":{"cards":[{"text":"Включаю."}],"directives":[
		{"name":"tts_play_placeholder","sub_name":"tts_play_placeholder","type":"client_action","payload":{"channel":"dialog"}},
		{"name":"audio_play","sub_name":"music","type":"client_action","payload":{"metadata":{"title":"Song One","subtitle":"Artist A",
			"glagol_metadata":{"music_metadata":{"id":"user:onyourwave","type":"Radio"}}},"stream":{"id":"152653243"}}}]}}`
	nativeArtist = `{"response":{"directives":[{"name":"audio_play","sub_name":"music","type":"client_action","payload":{"metadata":{"title":"Song Two","subtitle":"Artist B",
		"glagol_metadata":{"music_metadata":{"id":"100","type":"Artist"}}}}}]}}`
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
		{nativeArtist, "Включи artist b", "play", "Artist B"},
		{`{"response":{"directives":[{"name":"audio_play","sub_name":"music","payload":{"metadata":{"title":"Song Two","subtitle":"Artist B","glagol_metadata":{"music_metadata":{"type":"Track"}}}}}]}}`, "включи сонг ту", "play", "Artist B Song Two"},
		{`{"response":{"directives":[{"name":"audio_play","sub_name":"music","payload":{"metadata":{"glagol_metadata":{"music_metadata":{"id":"genre:rock","type":"Radio"}}}}}]}}`, "Алиса, поставь рок", "play", "плейлист рок"},
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
	versions := []Track{{ID: "audio", Title: "Song One"}, {ID: "clip", Title: "Song One (Official Video)"}, {ID: "lyrics", Title: "Song One (Lyrics)"}}
	if got := singleTrack(versions, true); len(got) != 1 || got[0].ID != "audio" {
		t.Fatalf("single request queue = %+v, want only the best match", got)
	}
	if got := singleTrack(versions, false); len(got) != 3 {
		t.Fatalf("artist/album request queue = %d tracks, want all 3", len(got))
	}
}

func TestTakeoverRequestCarriesTitleAndSpokenFallbacks(t *testing.T) {
	r := takeoverRequest(nativeMusic{Kind: "Track", Title: "Alpha, Beta, Gamma", Artist: "Исполнитель"}, "алиса включи гамма")
	if r.Action != "play" || r.Query != "Исполнитель Alpha, Beta, Gamma" || !r.Single || r.Title != "Alpha, Beta, Gamma" || r.Artist != "Исполнитель" || r.Spoken != "гамма" {
		t.Fatalf("track request = %+v", r)
	}
	r = takeoverRequest(nativeMusic{Kind: "Artist", Artist: "Группа А"}, "алиса включи группа а")
	if r.Single || r.Title != "" || r.Query != "Группа А" {
		t.Fatalf("artist request = %+v", r)
	}
	if r = takeoverRequest(nativeMusic{Kind: "Radio", ID: "user:onyourwave"}, "алиса включи мою волну"); r.Action != "recommendations" || r.Spoken != "" {
		t.Fatalf("wave request = %+v", r)
	}
}

func TestSameTitleTrustsOnlyDistinctiveTitlesFromOtherArtists(t *testing.T) {
	// One catalog credits the song to «Исполнитель», the other to «Group B».
	distinctive := []Track{
		{ID: "track-1", Artists: "Group B", Title: "Alpha, Beta, Gamma"},
		{ID: "track-2", Artists: "Group B", Title: "Alpha Beta Gamma (Slowed)"},
		{ID: "track-3", Artists: "Group C", Title: "All Night Alpha"},
		{ID: "track-4", Artists: "Group D", Title: "Alpha Beta Gamma"},
	}
	if got := sameTitle("Исполнитель", "Alpha, Beta, Gamma", distinctive); len(got) != 2 || got[0].ID != "track-1" {
		t.Fatalf("distinctive title: %+v", got)
	}
	short := []Track{{ID: "abcdefghijk", Artists: "Другой исполнитель", Title: "Звезда"}, {ID: "bcdefghijkl", Artists: "Группа А", Title: "Звезда"}}
	if got := sameTitle("Группа А", "Звезда", short); len(got) != 1 || got[0].ID != "bcdefghijkl" {
		t.Fatalf("same artist must win: %+v", got)
	}
	if got := sameTitle("Группа А", "Звезда", short[:1]); len(got) != 0 {
		t.Fatalf("a one-word title of another artist was accepted: %+v", got)
	}
}

func TestTakeoverLookupLogNamesRequestResultAndStep(t *testing.T) {
	r := request{Query: "Исполнитель Alpha, Beta, Gamma", Title: "Alpha, Beta, Gamma", Artist: "Исполнитель", Spoken: "гамма"}
	got := takeoverLookupLog(r, "title", []Track{{Artists: "Group B", Title: "Alpha, Beta, Gamma"}}, nil)
	want := `voice takeover: "Исполнитель — Alpha, Beta, Gamma" (said "гамма") -> "Group B — Alpha, Beta, Gamma" by title`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := takeoverLookupLog(request{Query: "звезда", Spoken: "звезда"}, "query", nil, errNoTracks); got != `voice takeover: "звезда" (said "звезда") not found` {
		t.Fatal(got)
	}
}

func TestCloudAnswerSeesNotFoundAndMusic(t *testing.T) {
	// Shape recorded on a Mini 2: Alice answered «Не нашла ничего похожего»
	// to a song request and kept listening.
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
		"Алиса, включи artist a song one": "artist a song one",
		"поставь группу а":                "группу а",
		"включи свет":                     "",
		"включи таймер на 5 минут":        "",
		"включи музыку":                   "",
		"включи мою волну":                "",
		"сколько времени":                 "",
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
		// The catalog picked a different song: one named after the artist,
		// or another song of the artist.
		{"Other Band", "Singer", "включи singer song two", true},
		{"Singer", "Rain", "включи singer трек song one", true},
		// The catalog song matches, also when said in Cyrillic.
		{"Исполнитель", "Alpha, Beta, Gamma", "включи гамма", false},
		{"Группа А", "Утро", "включи утро", false},
		{"Singer", "Song One", "включи сингер song one", false},
	} {
		r := takeoverRequest(nativeMusic{Kind: "Track", Artist: tc.artist, Title: tc.title}, tc.said)
		if r.SpokenFirst != tc.first {
			t.Errorf("%q vs %s — %s: spoken first = %v, want %v", tc.said, tc.artist, tc.title, r.SpokenFirst, tc.first)
		}
	}
	// A misheard foreign title does not match the catalog, so the spoken words
	// are tried first and the catalog song remains the fallback.
	if r := takeoverRequest(nativeMusic{Kind: "Track", Artist: "Band X", Title: "Song Title"}, "включи сонг тайтл"); !r.SpokenFirst || r.Query != "Band X Song Title" {
		t.Fatalf("misheard title: %+v", r)
	}
}

func TestSpokenQueryDropsFillerWordsInside(t *testing.T) {
	for text, want := range map[string]string{
		"включи artist a трек song one":         "artist a song one",
		"Алиса, включи artist a песня song one": "artist a song one",
		"включи трек song two":                  "song two",
		"включи песню":                          "",
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

func TestAlbumTakeoverSearchesAnAlbum(t *testing.T) {
	// The cloud chose an Album; searched as a song the phrase found only the
	// title track.
	for text, want := range map[string]string{
		"включи artist a альбом album one": "альбом artist a album one",
		"Включи альбом album two artist b": "альбом album two artist b",
		"включи album two":                 "альбом album two",
	} {
		if a, q := takeoverAction(nativeMusic{Kind: "Album", Title: "Album One", Artist: "Artist A"}, text); a != "play" || q != want {
			t.Errorf("%q: got %s %q, want %q", text, a, q, want)
		}
	}
	if r := takeoverRequest(nativeMusic{Kind: "Album"}, "включи artist a альбом album one"); r.Single || r.SpokenFirst {
		t.Fatalf("an album is not one song: %+v", r)
	}
}

func TestSaidAlbumWinsOverCatalogTrack(t *testing.T) {
	r := takeoverRequest(nativeMusic{Kind: "Track", Artist: "Artist A", Title: "Album One"}, "Включи artist a альбом album one")
	if r.Query != "альбом artist a album one" || r.Single || r.SpokenFirst || r.Title != "" {
		t.Fatalf("%+v", r)
	}
	if r := takeoverRequest(nativeMusic{Kind: "Track", Artist: "Группа А", Title: "Утро"}, "включи утро"); r.Query != "Группа А Утро" || !r.Single {
		t.Fatalf("no «альбом», catalog track: %+v", r)
	}
}

func TestGenresAndChartsBecomePlaylists(t *testing.T) {
	for _, tc := range []struct{ kind, text, action, query string }{
		{"Radio", "включи рок", "play", "плейлист рок"},
		{"Radio", "включи что-нибудь весёлое", "play", "плейлист весёлое"},
		{"Playlist", "включи чарт", "play", "плейлист чарт"},
		{"Radio", "включи радио", "recommendations", ""},
	} {
		if a, q := takeoverAction(nativeMusic{Kind: tc.kind, ID: "genre:x"}, tc.text); a != tc.action || q != tc.query {
			t.Errorf("%q (%s) => %s %q, want %s %q", tc.text, tc.kind, a, q, tc.action, tc.query)
		}
	}
	if r := takeoverRequest(nativeMusic{Kind: "Track", Artist: "A", Title: "B"}, "включи плейлист для бега"); r.Query != "плейлист для бега" || r.Single {
		t.Fatalf("said «плейлист»: %+v", r)
	}
}

func TestSpokenFirstWhenCatalogPickedAnotherArtist(t *testing.T) {
	// The catalog answered with another artist than the one said.
	if r := takeoverRequest(nativeMusic{Kind: "Artist", Artist: "Artist B"}, "включи artist a"); !r.SpokenFirst || r.Query != "Artist B" || r.Spoken != "artist a" || r.Single {
		t.Fatalf("%+v", r)
	}
	if r := takeoverRequest(nativeMusic{Kind: "Artist", Artist: "Группа А"}, "включи группа а"); r.SpokenFirst {
		t.Fatalf("matching artist: %+v", r)
	}
}
