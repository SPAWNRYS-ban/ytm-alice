package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// nativeMusic is what Alice's cloud chose to play with its own music player.
// With youtube_only the request is cancelled and replayed on YouTube Music.
type nativeMusic struct {
	Kind   string // Radio, Artist, Track, Album, Playlist, …
	ID     string // user:onyourwave for «Моя волна»
	Title  string
	Artist string
}

// parseNativeMusic reads the AliceResponse JSON of a BUSY event. Only the
// audio_play music directive counts: smart home, timers or speech have none.
func parseNativeMusic(raw []byte) (nativeMusic, bool) {
	if len(raw) == 0 || !bytes.Contains(raw, []byte(`"audio_play"`)) {
		return nativeMusic{}, false
	}
	var reply struct {
		Response struct {
			Directives []struct {
				Name    string `json:"name"`
				SubName string `json:"sub_name"`
				Payload struct {
					Metadata struct {
						Title    string `json:"title"`
						Subtitle string `json:"subtitle"`
						Glagol   struct {
							Music struct {
								ID   string `json:"id"`
								Type string `json:"type"`
							} `json:"music_metadata"`
						} `json:"glagol_metadata"`
					} `json:"metadata"`
				} `json:"payload"`
			} `json:"directives"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return nativeMusic{}, false
	}
	for _, d := range reply.Response.Directives {
		if d.Name != "audio_play" || d.SubName != "music" {
			continue
		}
		m := d.Payload.Metadata
		return nativeMusic{Kind: m.Glagol.Music.Type, ID: m.Glagol.Music.ID, Title: strings.TrimSpace(m.Title), Artist: strings.TrimSpace(m.Subtitle)}, true
	}
	return nativeMusic{}, false
}

// takeoverAction maps the cloud's choice to a YouTube Music command. A track
// or an artist is searched by its catalog name, which is more precise than the
// spoken words; «Моя волна» becomes personal recommendations.
func takeoverAction(m nativeMusic, text string) (string, string) {
	switch {
	case m.Kind == "Radio" && m.ID == "user:onyourwave":
		return "recommendations", ""
	case m.Kind == "Track" && m.Title != "":
		return "play", strings.TrimSpace(m.Artist + " " + m.Title)
	case m.Kind == "Artist" && m.Artist != "":
		return "play", m.Artist
	case m.Kind == "Playlist" || m.Kind == "Radio":
		// A genre, mood or chart: YouTube Music has playlists, not the
		// catalog's radio. «включи рок» searched as a song played «Лето,
		// плавки, рок-н-ролл».
		if query := playlistQuery(text); query != "" {
			return "play", query
		}
		return "recommendations", ""
	case m.Kind == "Album":
		// The audio_play metadata names the first track, not the album, so
		// search an album by the spoken words.
		if query := albumQuery(text); query != "" {
			return "play", query
		}
	}
	if query := spokenMusicQuery(text); query != "" {
		return "play", query
	}
	return "recommendations", ""
}

// takeoverRequest builds the YouTube command for the cloud's choice. Alice
// picked one song: play just that (radio continues), by its catalog name, then
// by its title alone, then by the spoken words.
func takeoverRequest(m nativeMusic, text string) request {
	// «альбом» or «плейлист» said aloud wins over the catalog's kind of answer.
	if saysAlbum(text) {
		if query := albumQuery(text); query != "" {
			return request{Action: "play", Query: query, Spoken: spokenMusicQuery(text)}
		}
	}
	if saysPlaylist(text) {
		if query := playlistQuery(text); query != "" {
			return request{Action: "play", Query: query, Spoken: spokenMusicQuery(text)}
		}
	}
	action, query := takeoverAction(m, text)
	r := request{Action: action, Query: query}
	if action == "play" && m.Kind == "Track" && m.Title != "" {
		r.Single, r.Title, r.Artist = true, m.Title, m.Artist
	}
	if action == "play" {
		r.Spoken = spokenMusicQuery(text)
	}
	// When the catalog song does not match what was said, try the spoken
	// words first; the catalog song stays the fallback (it also fixes
	// misheard names such as «мистер брайтсайд» -> «Mr. Brightside»).
	r.SpokenFirst = r.Single && r.Spoken != "" && !coveredBy(r.Spoken, m.Artist+" "+m.Title)
	// The same for an artist: «charli xcx» came back as Icona Pop.
	if action == "play" && m.Kind == "Artist" && r.Spoken != "" && !coveredBy(r.Spoken, m.Artist) {
		r.SpokenFirst = true
	}
	return r
}

// spokenMusicQuery strips the request words, leaving what to search for.
// An empty result means «any music».
func spokenMusicQuery(text string) string {
	q := strings.TrimSpace(strings.TrimPrefix(normalize(text), "алиса "))
	q = strings.TrimSpace(strings.TrimSuffix(q, " пожалуйста"))
	for _, verb := range []string{"включи ", "поставь ", "запусти ", "сыграй ", "найди ", "играй "} {
		if strings.HasPrefix(q, verb) {
			q = strings.TrimSpace(q[len(verb):])
			break
		}
	}
	q = withoutYouTube(q)
	for _, noun := range []string{"песню ", "трек ", "композицию ", "музыку "} {
		q = strings.TrimSpace(strings.TrimPrefix(q, noun))
	}
	switch q {
	case "музыку", "песню", "трек", "что-нибудь", "мою волну", "волну", "мою музыку":
		return ""
	}
	// «madonna трек everything»: a filler word inside the phrase is not part
	// of the artist or title either.
	words := []string{}
	for _, w := range strings.Fields(q) {
		switch w {
		case "трек", "песню", "песня", "композицию", "композиция":
			continue
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

// coveredBy reports that the catalog song contains the spoken words. The
// catalog sometimes picks a different song: «madonna fragile» became
// «Umideep — Madonna», a song named after the artist.
func coveredBy(spoken, song string) bool {
	words := searchWords(spoken)
	if len(words) == 0 {
		return true
	}
	have := map[string]bool{}
	for _, w := range searchWords(song) {
		have[w] = true
	}
	matched := 0
	for _, w := range words {
		if have[w] {
			matched++
		}
	}
	return float64(matched)/float64(len(words)) >= 0.8
}

// singleTrack keeps only the best match of a one-song request. Matching
// results for "artist title" are mostly the same song again (official audio,
// clip, lyrics, live), which made the request sound like it played on repeat.
func singleTrack(tracks []Track, single bool) []Track {
	if single && len(tracks) > 1 {
		return tracks[:1]
	}
	return tracks
}

// takeoverLookupLog records what Alice's catalog asked for and what YouTube
// Music played instead, so a wrong or missing song can be traced.
func takeoverLookupLog(r request, step string, tracks []Track, err error) string {
	asked := strconv.Quote(r.Query)
	if r.Title != "" {
		asked = strconv.Quote(strings.TrimSpace(r.Artist + " — " + r.Title))
	}
	if r.Spoken != "" {
		asked += " (said " + strconv.Quote(r.Spoken) + ")"
	}
	if err != nil || len(tracks) == 0 {
		return "voice takeover: " + asked + " not found"
	}
	return "voice takeover: " + asked + " -> " + strconv.Quote(tracks[0].Artists+" — "+tracks[0].Title) + " by " + step
}

// cloudAnswer reads a BUSY response: whether the cloud chose music
// (audio_play) and whether Alice keeps listening for a clarification.
func cloudAnswer(raw []byte) (music, listen bool) {
	var reply struct {
		Response struct {
			Directives []struct {
				Name string `json:"name"`
			} `json:"directives"`
		} `json:"response"`
		VoiceResponse struct {
			ShouldListen bool `json:"should_listen"`
		} `json:"voice_response"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &reply) != nil {
		return false, false
	}
	for _, d := range reply.Response.Directives {
		if d.Name == "audio_play" {
			music = true
		}
	}
	return music, reply.VoiceResponse.ShouldListen
}

// Requests that start with a play verb but are not about music.
var notMusic = []string{
	"свет", "ламп", "люстр", "подсвет", "телевизор", "тв ", "таймер", "будильник",
	"громк", "звук", "тише", "громче", "кондиционер", "пылесос", "розетк", "обогрев",
	"увлажн", "чайник", "бойлер", "отоплен", "вентилятор", "блютуз", "bluetooth",
	"режим", "радио", "сказк", "игр", "новост", "погод", "мультик", "фильм", "канал",
}

// musicRequest returns what to search for when the phrase asks to play
// something («включи madonna everything» -> «madonna everything»).
func musicRequest(text string) (string, bool) {
	q := strings.TrimSpace(strings.TrimPrefix(normalize(text), "алиса "))
	verb := false
	for _, v := range []string{"включи ", "поставь ", "запусти ", "сыграй "} {
		if strings.HasPrefix(q, v) {
			verb = true
			break
		}
	}
	query := spokenMusicQuery(text)
	if !verb || query == "" {
		return "", false
	}
	for _, token := range strings.Fields(query) {
		for _, word := range notMusic {
			if strings.HasPrefix(token+" ", word) {
				return "", false
			}
		}
	}
	return query, true
}

var urlInText = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://\S+`)

// logSafe keeps a command error readable in the log without the request
// URLs that Go network errors embed (they can carry signatures and keys).
func logSafe(message string) string {
	message = urlInText.ReplaceAllString(message, "<url>")
	if len([]rune(message)) > 200 {
		message = string([]rune(message)[:200]) + "…"
	}
	return message
}

// albumQuery turns «troye sivan альбом she s the best» into an album search,
// «альбом troye sivan she s the best»: searched as a song it found only the
// title track.
func albumQuery(text string) string {
	words := []string{}
	for _, w := range strings.Fields(spokenMusicQuery(text)) {
		if w != "альбом" && w != "альбома" && w != "альбоме" {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return ""
	}
	return "альбом " + strings.Join(words, " ")
}

func saysAlbum(text string) bool {
	for _, w := range strings.Fields(normalize(text)) {
		if w == "альбом" || w == "альбома" || w == "альбоме" {
			return true
		}
	}
	return false
}

// playlistQuery turns a genre, mood or chart request into a playlist search:
// «включи рок» -> «плейлист рок», «включи что-нибудь весёлое» -> «плейлист
// весёлое».
func playlistQuery(text string) string {
	words := []string{}
	for _, w := range strings.Fields(spokenMusicQuery(text)) {
		switch w {
		case "плейлист", "плейлиста", "радио", "что-нибудь", "что-то", "музыку", "музыка":
			continue
		}
		words = append(words, w)
	}
	if len(words) == 0 {
		return ""
	}
	return "плейлист " + strings.Join(words, " ")
}

func saysPlaylist(text string) bool {
	for _, w := range strings.Fields(normalize(text)) {
		if w == "плейлист" || w == "плейлиста" {
			return true
		}
	}
	return false
}
