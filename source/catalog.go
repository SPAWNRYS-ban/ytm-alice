package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	yt "github.com/kkdai/youtube/v2"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Track struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Artists      string `json:"artists"`
	DurationMS   int64  `json:"duration_ms"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
}

// Only return image URLs. Image bytes are fetched and cached by the browser.
func safeThumbnail(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || len(raw) > 2048 {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	for _, domain := range []string{"ytimg.com", "googleusercontent.com", "ggpht.com"} {
		if h == domain || strings.HasSuffix(h, "."+domain) {
			return raw
		}
	}
	return ""
}

func fallbackThumbnail(id string) string {
	if !videoID.MatchString(id) {
		return ""
	}
	return "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
}

func thumbnailURL(item any) string {
	paths := [][]any{{"thumbnail", "musicThumbnailRenderer", "thumbnail", "thumbnails"}, {"thumbnail", "thumbnails"}, {"thumbnails"}}
	for _, path := range paths {
		images, ok := get(item, path...).([]any)
		if !ok {
			continue
		}
		best, bestScore := "", 1<<30
		for _, im := range images {
			u := safeThumbnail(str(get(im, "url")))
			if u == "" {
				continue
			}
			width, _ := get(im, "width").(float64)
			score := int(width) - 512
			if score < 0 {
				score = -score
			}
			if score < bestScore {
				best, bestScore = u, score
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

type Catalog struct {
	http          *http.Client
	playerSource  string
	mu            sync.Mutex
	cache         map[string]catalogEntry
	streams       map[string]streamInfo
	authMu        sync.RWMutex
	authChange    sync.Mutex
	auth          *Credentials
	authEpoch     atomic.Uint64
	authResult    AuthStatus
	authCheckedAt time.Time
	// YouTube TV client and its short-lived access token (see tv_oauth.go).
	tvMu          sync.Mutex
	tvClient      tvClient
	tvClientAt    time.Time
	tvAccess      string
	tvAccessFor   string
	tvAccessUntil time.Time
	tvLoginMu     sync.Mutex
	tvLogin       TVLoginState
	tvLoginCancel context.CancelFunc
	tvLoginID     uint64
	// YouTube answers 429 to a rate-limited IP; back off instead of retrying.
	limitMu      sync.Mutex
	limitedUntil time.Time
	limitBackoff time.Duration
	// One library client for all tracks so its visitor id and player JS caches work.
	ytMu      sync.Mutex
	ytClient  *yt.Client
	ytCreated time.Time
}
type catalogEntry struct {
	tracks []Track
	until  time.Time
}
type streamInfo struct {
	url, mime                     string
	length                        int64
	until                         time.Time
	id                            string
	initEnd, indexStart, indexEnd int64
}

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func init() {
	// ANDROID_VR URLs can stop serving ranges beyond the preview window.
	// VISIONOS supports full-file playback and seeking without cookies/PO tokens.
	yt.DefaultClient = yt.ClientInfo{Name: "VISIONOS", Version: "1.02", DeviceModel: "RealityDevice17,1", UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_7_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"}
}

func get(x any, path ...any) any {
	for _, k := range path {
		switch key := k.(type) {
		case string:
			m, ok := x.(map[string]any)
			if !ok {
				return nil
			}
			x = m[key]
		case int:
			a, ok := x.([]any)
			if !ok || key < 0 || key >= len(a) {
				return nil
			}
			x = a[key]
		}
	}
	return x
}
func str(x any) string { s, _ := x.(string); return s }
func text(x any) string {
	if s := str(get(x, "simpleText")); s != "" {
		return s
	}
	var parts []string
	if a, ok := get(x, "runs").([]any); ok {
		for _, v := range a {
			parts = append(parts, str(get(v, "text")))
		}
	}
	return strings.Join(parts, "")
}
func clockMS(s string) int64 {
	var n int64
	for _, v := range strings.Split(s, ":") {
		p, e := strconv.ParseInt(v, 10, 64)
		if e != nil {
			return 0
		}
		n = n*60 + p
	}
	return n * 1000
}
func firstKey(x any, key string) string {
	switch v := x.(type) {
	case map[string]any:
		if s := str(v[key]); s != "" {
			return s
		}
		// Sort-priority avoids taking a recommendation ID from a context menu.
		for _, k := range []string{"playlistItemData", "navigationEndpoint", "overlay", "flexColumns", "playNavigationEndpoint", "watchEndpoint", "browseEndpoint", "runs"} {
			if s := firstKey(v[k], key); s != "" {
				return s
			}
		}
	case []any:
		for _, a := range v {
			if s := firstKey(a, key); s != "" {
				return s
			}
		}
	}
	return ""
}
func parseTracks(root any) []Track {
	tracks := []Track{}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(x any) {
		switch v := x.(type) {
		case map[string]any:
			if z, ok := v["tileRenderer"]; ok {
				if t, ok := parseTile(z); ok && !seen[t.ID] {
					seen[t.ID] = true
					tracks = append(tracks, t)
				}
				return
			}
			for _, kind := range []string{"musicResponsiveListItemRenderer", "playlistPanelVideoRenderer", "musicTwoRowItemRenderer"} {
				if z, ok := v[kind]; ok {
					id := firstKey(z, "videoId")
					if videoID.MatchString(id) && !seen[id] {
						title := text(get(z, "flexColumns", 0, "musicResponsiveListItemFlexColumnRenderer", "text"))
						if title == "" {
							title = text(get(z, "title"))
						}
						artists := text(get(z, "longBylineText"))
						duration := text(get(z, "lengthText"))
						if a, ok := get(z, "flexColumns", 1, "musicResponsiveListItemFlexColumnRenderer", "text", "runs").([]any); ok {
							var names []string
							for _, run := range a {
								bid := str(get(run, "navigationEndpoint", "browseEndpoint", "browseId"))
								s := str(get(run, "text"))
								if strings.HasPrefix(bid, "UC") {
									names = append(names, s)
								}
								if strings.Contains(s, ":") && clockMS(s) > 0 {
									duration = s
								}
							}
							artists = strings.Join(names, ", ")
						}
						if duration == "" {
							duration = text(get(z, "fixedColumns", 0, "musicResponsiveListItemFixedColumnRenderer", "text"))
						}
						if title == "" {
							title = id
						}
						t := Track{ID: id, Title: title, Artists: artists, DurationMS: clockMS(duration), ThumbnailURL: thumbnailURL(z)}
						if t.ThumbnailURL == "" {
							t.ThumbnailURL = fallbackThumbnail(id)
						}
						if t.DurationMS <= 7200000 {
							tracks = append(tracks, t)
							seen[id] = true
						}
					}
					return
				}
			}
			// JSON objects are unordered; preserve the order in their contents arrays.
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(v[k])
			}
		case []any:
			for _, z := range v {
				walk(z)
			}
		}
	}
	walk(root)

	return tracks
}
func (c *Catalog) request(ctx context.Context, endpoint string, body map[string]any) (map[string]any, error) {
	return c.requestWithAuth(ctx, endpoint, body, c.credentials())
}
func (c *Catalog) requestWithAuth(ctx context.Context, endpoint string, body map[string]any, auth *Credentials) (map[string]any, error) {
	return c.requestWithClient(ctx, endpoint, body, auth, "WEB_REMIX")
}
func (c *Catalog) requestWithClient(ctx context.Context, endpoint string, body map[string]any, auth *Credentials, clientName string) (map[string]any, error) {
	if auth.isTV() {
		if tvServes(endpoint, body) {
			return c.tvRequest(ctx, endpoint, body, auth)
		}
		// Search, player and queue lookups go publicly, or with the optional cookies.
		auth = auth.cookieSession()
	}
	if endpoint == "player" {
		if err := c.playbackLimit(); err != nil {
			return nil, err
		}
	}
	prefix := "1."
	if clientName == "WEB" {
		prefix = "2."
	}
	user := map[string]any{}
	if auth != nil && auth.PageID != "" {
		user["onBehalfOfUser"] = auth.PageID
	}
	clientContext := map[string]any{"clientName": clientName, "clientVersion": prefix + time.Now().UTC().Format("20060102") + ".01.00", "hl": "ru", "gl": "US"}
	body["context"] = map[string]any{"client": clientContext, "user": user}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://music.youtube.com/youtubei/v1/"+endpoint+"?alt=json", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://music.youtube.com")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if auth != nil {
		auth.apply(req, time.Now())
	}
	client := *c.http
	// A login redirect must never receive a copied Cookie/Authorization header.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("YouTube Music: network unavailable")
	}
	defer resp.Body.Close()
	if endpoint == "player" && resp.StatusCode == http.StatusTooManyRequests {
		return nil, c.noteRateLimit()
	}
	if resp.StatusCode != 200 {
		if auth != nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
			c.setAuthResult(auth, nil, errSessionExpired)
			return nil, errSessionExpired
		}
		return nil, fmt.Errorf("YouTube Music: HTTP %d", resp.StatusCode)
	}
	var out map[string]any
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, errors.New("YouTube Music: некорректный ответ сервера")
	}
	if auth != nil && responseLoggedOut(out) {
		c.setAuthResult(auth, nil, errSessionExpired)
		return nil, errSessionExpired
	}
	return out, nil
}
func (c *Catalog) lookup(ctx context.Context, q string) ([]Track, error) {
	q = strings.TrimSpace(q)
	if q == "" || len([]rune(q)) > 500 {
		return nil, errors.New("нужен запрос до 500 символов")
	}
	queryKey := strings.ToLower(q)
	if isLikedQuery(q) {
		if err := c.requireAuth(ctx); err != nil {
			return nil, err
		}
	}
	key := fmt.Sprintf("%d:%s", c.authEpoch.Load(), queryKey)
	c.mu.Lock()
	hit, ok := c.cache[key]
	c.mu.Unlock()
	if ok && time.Now().Before(hit.until) {
		return append([]Track(nil), hit.tracks...), nil
	}
	var tracks []Track
	var err error
	u, _ := url.Parse(q)
	if isLikedQuery(q) {
		tracks, err = c.browse(ctx, "VLLM")
	} else if u.Scheme != "" {
		host := strings.ToLower(u.Hostname())
		if (u.Scheme != "http" && u.Scheme != "https") || !(host == "youtu.be" || host == "youtube.com" || host == "www.youtube.com" || host == "music.youtube.com") {
			return nil, errors.New("поддерживаются только ссылки YouTube")
		}
		if list := u.Query().Get("list"); list != "" {
			tracks, err = c.browse(ctx, "VL"+list)
		} else {
			id := u.Query().Get("v")
			if host == "youtu.be" {
				id = strings.Trim(u.Path, "/")
			}
			if !videoID.MatchString(id) {
				return nil, errors.New("неверный ID трека")
			}
			v, e := c.trackVideo(ctx, id)
			err = e
			if e == nil {
				tracks = []Track{{ID: id, Title: v.Title, Artists: v.Author, DurationMS: v.Duration.Milliseconds(), ThumbnailURL: fallbackThumbnail(id)}}
			}
		}
	} else {
		kind := "songs"
		term := q
		if strings.HasPrefix(queryKey, "альбом ") {
			kind = "albums"
			term = strings.TrimSpace(q[len("альбом "):])
		}
		if strings.HasPrefix(queryKey, "плейлист ") {
			kind = "playlists"
			term = strings.TrimSpace(q[len("плейлист "):])
		}
		params := "EgWKAQIIAWoMEA4QChADEAQQCRAF"
		if kind == "albums" {
			params = "EgWKAQIYAWoMEA4QChADEAQQCRAF"
		}
		if kind == "playlists" {
			params = "Eg-KAQwIABAAGAAgACgBMABqChAEEAMQCRAFEAo%3D"
		}
		var data map[string]any
		data, err = c.requestWithAuth(ctx, "search", map[string]any{"query": term, "params": params}, nil)
		if err == nil {
			if kind == "songs" {
				tracks = parseTracks(data)
			} else {
				id := findBrowse(data, kind)
				if id == "" {
					err = errors.New("альбом или плейлист не найден")
				} else {
					tracks, err = c.browse(ctx, id)
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, errNoTracks
	}
	c.mu.Lock()
	if len(c.cache) >= 64 {
		c.cache = map[string]catalogEntry{}
	}
	c.cache[key] = catalogEntry{append([]Track(nil), tracks...), time.Now().Add(15 * time.Minute)}
	c.mu.Unlock()
	return tracks, nil
}
func findBrowse(x any, kind string) string {
	switch v := x.(type) {
	case map[string]any:
		for _, k := range []string{"musicResponsiveListItemRenderer", "musicTwoRowItemRenderer"} {
			if z, ok := v[k]; ok {
				id := firstKey(z, "browseId")
				if kind == "albums" && strings.HasPrefix(id, "MPRE") {
					return id
				}
				if kind == "playlists" && (strings.HasPrefix(id, "VL") || strings.HasPrefix(id, "VM")) {
					return "VL" + strings.TrimPrefix(strings.TrimPrefix(id, "VL"), "VM")
				}
			}
		}
		for _, a := range v {
			if s := findBrowse(a, kind); s != "" {
				return s
			}
		}
	case []any:
		for _, a := range v {
			if s := findBrowse(a, kind); s != "" {
				return s
			}
		}
	}
	return ""
}
func (c *Catalog) browse(ctx context.Context, id string) ([]Track, error) {
	tracks := []Track{}
	seen := map[string]bool{}
	err := c.browsePages(ctx, id, func(page map[string]any) {
		for _, track := range parseTracks(page) {
			if !seen[track.ID] {
				seen[track.ID] = true
				tracks = append(tracks, track)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return tracks, nil
}
func (c *Catalog) radio(ctx context.Context, id string) []Track {
	auth := c.credentials()
	d, e := c.requestWithAuth(ctx, "next", map[string]any{"videoId": id, "playlistId": "RDAMVM" + id, "isAudioOnly": true}, auth)
	if e != nil {
		return nil
	}
	if auth.isTV() {
		// The TV watch page also lists unrelated suggestions; keep the radio queue.
		return parseTileQueue(d, "RDAMVM"+id)
	}
	return parseTracks(d)
}
func (c *Catalog) resolve(ctx context.Context, id string) (streamInfo, error) {
	if !videoID.MatchString(id) {
		return streamInfo{}, errors.New("неверный ID трека")
	}
	// Also bounds the peak memory of the JS resolver on a 256 MiB Station.
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.streams[id]; ok && time.Now().Before(s.until) {
		return s, nil
	}
	v, err := c.trackVideo(ctx, id)
	if err != nil {
		return streamInfo{}, err
	}
	formats := v.Formats.WithAudioChannels()
	formats.Sort()
	var streamErr error
	for _, f := range formats {
		if f.Width != 0 || f.AudioChannels == 0 {
			continue
		}
		// The firmware provides an AAC/MP4 decoder but no Matroska demuxer.
		if !strings.HasPrefix(f.MimeType, "audio/mp4") {
			continue
		}
		link := f.URL
		parsed, parseErr := url.Parse(link)
		var e error
		if link == "" || parseErr != nil || parsed.Query().Get("n") != "" {
			// Legacy decoding can return a malformed n/signature without an
			// error. Use the same current decoder for public and signed-in URLs.
			link, e = c.decipherURL(ctx, id, f)
		}
		if e != nil {
			streamErr = e
			continue
		}
		u, e := url.Parse(link)
		if e != nil || !validAudioURL(u) {
			continue
		}
		s := streamInfo{url: link, mime: f.MimeType, length: f.ContentLength, until: time.Now().Add(time.Hour), id: id}
		if f.InitRange != nil && f.IndexRange != nil {
			s.initEnd, _ = strconv.ParseInt(f.InitRange.End, 10, 64)
			s.indexStart, _ = strconv.ParseInt(f.IndexRange.Start, 10, 64)
			s.indexEnd, _ = strconv.ParseInt(f.IndexRange.End, 10, 64)
		}
		c.clearRateLimit()

		if len(c.streams) >= 12 {
			c.streams = map[string]streamInfo{}
		}
		c.streams[id] = s
		return s, nil
	}
	if streamErr != nil {
		return streamInfo{}, streamErr
	}
	return streamInfo{}, errors.New("YouTube: подходящий аудиопоток отсутствует")
}
