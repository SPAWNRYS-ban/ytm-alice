package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

// A voice transcript often spells a Latin artist in Cyrillic. Use a small,
// deterministic phonetic normalization rather than accepting a near-name artist.
func searchWords(value string) []string {
	latin := []string{"a", "b", "v", "g", "d", "e", "zh", "z", "i", "y", "k", "l", "m", "n", "o", "p", "r", "s", "t", "u", "f", "kh", "ts", "ch", "sh", "shch", "", "y", "", "e", "yu", "ya"}
	var out strings.Builder
	for _, ch := range strings.ToLower(value) {
		if ch == 'ё' {
			ch = 'е'
		}
		if ch >= 'а' && ch <= 'я' {
			out.WriteString(latin[ch-'а'])
		} else if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			out.WriteRune(ch)
		} else {
			out.WriteByte(' ')
		}
	}
	value = strings.NewReplacer("th", "z", "w", "v", "ay", "ai", "ey", "ai", "ei", "ai").Replace(out.String())
	return strings.Fields(value)
}
func relevantTracks(query string, tracks []Track) []Track {
	words := searchWords(query)
	if len(words) == 0 {
		return nil
	}
	type candidate struct {
		track Track
		score float64
	}
	found := []candidate{}
	for _, track := range tracks {
		available := map[string]bool{}
		for _, word := range searchWords(track.Artists + " " + track.Title) {
			available[word] = true
		}
		matched := 0
		for _, word := range words {
			if available[word] {
				matched++
			}
		}
		score := float64(matched) / float64(len(words))
		if score >= 0.8 {
			found = append(found, candidate{track, score})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].score > found[j].score })
	result := []Track{}
	seen := map[string]bool{}
	for _, c := range found {
		if seen[c.track.ID] {
			continue
		}
		seen[c.track.ID] = true
		result = append(result, c.track)
		if len(result) == 20 {
			break
		}
	}
	return result
}

// Music search may return a song merely mentioning a requested artist in its
// title. Prefer names/titles beginning with the query (or artist+title), not a
// passing mention such as "Мейдоведение — Yowai The Maid" by another artist.
func relevantSongs(query string, tracks []Track) []Track {
	normalized := strings.Join(searchWords(query), " ")
	result := []Track{}
	for _, track := range relevantTracks(query, tracks) {
		for _, candidate := range []string{track.Artists, track.Title, track.Artists + " " + track.Title, track.Title + " " + track.Artists} {
			value := strings.Join(searchWords(candidate), " ")
			if value == normalized || strings.HasPrefix(value, normalized+" ") {
				result = append(result, track)
				break
			}
		}
	}
	return result
}

// Voice playback prefers matching Music songs, then matching public YouTube
// videos. Browsing/search UI keeps its complete Music results.
func (c *Catalog) playLookup(ctx context.Context, query string) ([]Track, error) {
	tracks, err := c.lookup(ctx, query)
	q := strings.TrimSpace(query)
	parsed, _ := url.Parse(q)
	lower := strings.ToLower(q)
	if parsed.Scheme != "" || isLikedQuery(q) || strings.HasPrefix(lower, "альбом ") || strings.HasPrefix(lower, "плейлист ") {
		return tracks, err
	}
	if err != nil && !errors.Is(err, errNoTracks) {
		return nil, err
	}
	if matching := relevantSongs(q, tracks); len(matching) > 0 {
		return matching, nil
	}
	videos, err := c.searchVideos(ctx, q)
	if err != nil {
		return nil, err
	}
	// The video provider need not translate an artist's Cyrillic phonetic
	// spelling. Search that spelling in Latin too, then prefer artist/title
	// prefixes over a song merely mentioning the requested artist.
	if strings.IndexFunc(q, func(ch rune) bool { return unicode.In(ch, unicode.Cyrillic) }) >= 0 {
		alternative, alternativeErr := c.searchVideos(ctx, strings.Join(searchWords(q), " "))
		if alternativeErr == nil {
			videos = append(videos, alternative...)
		}
	}
	if matching := relevantSongs(q, videos); len(matching) > 0 {
		return matching, nil
	}
	if matching := relevantTracks(q, videos); len(matching) > 0 {
		return matching, nil
	}
	return nil, errNoTracks
}

func (c *Catalog) searchVideos(ctx context.Context, query string) ([]Track, error) {
	payload := map[string]any{"query": query, "params": "EgIQAQ%3D%3D", "context": map[string]any{"client": map[string]any{"clientName": "WEB", "clientVersion": "2." + time.Now().UTC().Format("20060102") + ".01.00", "hl": "ru", "gl": "US"}}}
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://www.youtube.com/youtubei/v1/search", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("YouTube: поиск видео временно недоступен")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("YouTube: поиск видео HTTP %d", response.StatusCode)
	}
	var root map[string]any
	if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&root) != nil {
		return nil, errors.New("YouTube: некорректный ответ поиска видео")
	}
	result := []Track{}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if renderer, ok := v["videoRenderer"]; ok {
				id := str(get(renderer, "videoId"))
				title := text(get(renderer, "title"))
				duration := clockMS(text(get(renderer, "lengthText")))
				if videoID.MatchString(id) && !seen[id] && title != "" && duration > 0 && duration <= 7200000 {
					artist := text(get(renderer, "ownerText"))
					if artist == "" {
						artist = text(get(renderer, "shortBylineText"))
					}
					result = append(result, Track{ID: id, Title: title, Artists: artist, DurationMS: duration, ThumbnailURL: thumbnailURL(renderer)})
					seen[id] = true
				}
				return
			}
			for _, key := range sortedKeys(v) {
				walk(v[key])
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(root)
	return result, nil
}
