package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Personal requests of a YouTube TV sign-in. The Music web client rejects TV
// tokens (HTTP 400), so these go to the TVHTML5 client on www.youtube.com and
// return tileRenderer items instead of Music shelves.
var tvEndpoints = map[string]bool{"browse": true, "next": true}

// Albums and Music pages exist only in the Music client; read them publicly.
func tvServes(endpoint string, body map[string]any) bool {
	id, _ := body["browseId"].(string)
	return tvEndpoints[endpoint] && !strings.HasPrefix(id, "MPRE") && !strings.HasPrefix(id, "FEmusic_")
}

func (c *Catalog) tvRequest(ctx context.Context, endpoint string, body map[string]any, auth *Credentials) (map[string]any, error) {
	payload := map[string]any{}
	for key, value := range body {
		if key != "context" && key != "isAudioOnly" {
			payload[key] = value
		}
	}
	payload["context"] = map[string]any{"client": map[string]any{"clientName": "TVHTML5", "clientVersion": "7." + time.Now().UTC().Format("20060102") + ".00.00", "hl": "ru", "gl": "US"}}
	raw, _ := json.Marshal(payload)
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for attempt := 0; ; attempt++ {
		access, err := c.tvAccessToken(ctx, auth.TV)
		if err != nil {
			if errors.Is(err, errSessionExpired) {
				c.setAuthResult(auth, nil, err)
			}
			return nil, err
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", tvOrigin+"/youtubei/v1/"+endpoint+"?prettyPrint=false", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", tvUserAgent)
		req.Header.Set("Authorization", "Bearer "+access)
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.New("YouTube: network unavailable")
		}
		if resp.StatusCode == 401 && attempt == 0 {
			// The access token may have been revoked early; refresh it once.
			resp.Body.Close()
			c.forgetTVAccess()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			c.setAuthResult(auth, nil, errTVExpired)
			return nil, errTVExpired
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("YouTube: HTTP %d", resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
			return nil, errors.New("YouTube: некорректный ответ сервера")
		}
		if responseLoggedOut(out) {
			c.setAuthResult(auth, nil, errTVExpired)
			return nil, errTVExpired
		}
		return out, nil
	}
}

func tileDuration(tile any) string {
	overlays, _ := get(tile, "header", "tileHeaderRenderer", "thumbnailOverlays").([]any)
	for _, overlay := range overlays {
		if s := text(get(overlay, "thumbnailOverlayTimeStatusRenderer", "text")); s != "" {
			return s
		}
	}
	return ""
}

func parseTile(tile any) (Track, bool) {
	if kind := str(get(tile, "contentType")); kind != "" && kind != "TILE_CONTENT_TYPE_VIDEO" {
		return Track{}, false
	}
	id := str(get(tile, "onSelectCommand", "watchEndpoint", "videoId"))
	if !videoID.MatchString(id) {
		return Track{}, false
	}
	meta := get(tile, "metadata", "tileMetadataRenderer")
	t := Track{ID: id, Title: text(get(meta, "title")), Artists: text(get(meta, "lines", 0, "lineRenderer", "items", 0, "lineItemRenderer", "text")), DurationMS: clockMS(tileDuration(tile)), ThumbnailURL: thumbnailURL(get(tile, "header", "tileHeaderRenderer"))}
	if t.Title == "" {
		t.Title = id
	}
	if t.ThumbnailURL == "" {
		t.ThumbnailURL = fallbackThumbnail(id)
	}
	return t, t.DurationMS <= 7200000
}

// parseTileQueue keeps tiles of one watch queue, in page order.
func parseTileQueue(root any, playlist string) []Track {
	tracks := []Track{}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(x any) {
		switch v := x.(type) {
		case map[string]any:
			if tile, ok := v["tileRenderer"]; ok {
				if str(get(tile, "onSelectCommand", "watchEndpoint", "playlistId")) == playlist {
					if t, ok := parseTile(tile); ok && !seen[t.ID] {
						seen[t.ID] = true
						tracks = append(tracks, t)
					}
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
	return tracks
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func parsePlaylistTile(tile any) (Playlist, bool) {
	if str(get(tile, "contentType")) != "TILE_CONTENT_TYPE_PLAYLIST" {
		return Playlist{}, false
	}
	id := str(get(tile, "onSelectCommand", "watchEndpoint", "playlistId"))
	if id == "" {
		id = strings.TrimPrefix(str(get(tile, "onSelectCommand", "browseEndpoint", "browseId")), "VL")
	}
	// RD… are generated mixes, not playlists of the account.
	title := text(get(tile, "metadata", "tileMetadataRenderer", "title"))
	if !playlistID.MatchString(id) || strings.HasPrefix(id, "RD") || title == "" {
		return Playlist{}, false
	}
	return Playlist{ID: id, Title: title, ThumbnailURL: thumbnailURL(get(tile, "header", "tileHeaderRenderer"))}, true
}

func (c *Catalog) tvLibrary(ctx context.Context) ([]Playlist, error) {
	data, err := c.request(ctx, "browse", map[string]any{"browseId": "FElibrary"})
	if err != nil {
		return nil, err
	}
	return parsePlaylists(data), nil
}

// The TV client has no Music home page. A radio from the latest liked track
// is the closest personal mix it offers.
func (c *Catalog) tvRecommendations(ctx context.Context) ([]Track, error) {
	data, err := c.request(ctx, "browse", map[string]any{"browseId": "VLLM"})
	if err != nil {
		return nil, err
	}
	liked := parseTracks(browseContents(data))
	if len(liked) == 0 {
		return nil, errors.New("Для рекомендаций при входе через YouTube TV нужен хотя бы один лайк в YouTube Music")
	}
	tracks := c.radio(ctx, liked[0].ID)
	if len(tracks) == 0 {
		return nil, errors.New("YouTube не вернул рекомендации для этого аккаунта")
	}
	return tracks, nil
}

func (c *Catalog) tvAccountInfo(ctx context.Context, auth *Credentials) (*YouTubeAccount, error) {
	data, err := c.tvRequest(ctx, "account/accounts_list", map[string]any{}, auth)
	if err != nil {
		return nil, err
	}
	account := YouTubeAccount{AuthUser: "0", Name: "Аккаунт Google (YouTube TV)", Selected: true}
	var walk func(any) bool
	walk = func(x any) bool {
		switch v := x.(type) {
		case map[string]any:
			if name := text(v["accountName"]); name != "" {
				account.Name = name
				account.Handle = text(v["channelHandle"])
				account.ThumbnailURL = thumbnailURL(map[string]any{"thumbnail": v["accountPhoto"]})
				return true
			}
			for _, key := range sortedKeys(v) {
				if walk(v[key]) {
					return true
				}
			}
		case []any:
			for _, item := range v {
				if walk(item) {
					return true
				}
			}
		}
		return false
	}
	walk(data)
	return &account, nil
}
