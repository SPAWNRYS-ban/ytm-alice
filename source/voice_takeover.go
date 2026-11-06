package main

import (
	"bytes"
	"encoding/json"
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
	}
	if query := spokenMusicQuery(text); query != "" {
		return "play", query
	}
	return "recommendations", ""
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
	return q
}
