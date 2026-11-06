package main

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// Continuations belong to shelves, not navigation links or context menus.
func continuationToken(root any) string {
	switch v := root.(type) {
	case map[string]any:
		if token := str(get(v, "continuationItemRenderer", "continuationEndpoint", "continuationCommand", "token")); token != "" {
			return token
		}
		for _, kind := range []string{"nextContinuationData", "reloadContinuationData"} {
			if token := str(get(v, kind, "continuation")); token != "" {
				return token
			}
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key == "menu" || key == "navigationEndpoint" || key == "header" {
				continue
			}
			if token := continuationToken(v[key]); token != "" {
				return token
			}
		}
	case []any:
		for _, item := range v {
			if token := continuationToken(item); token != "" {
				return token
			}
		}
	}
	return ""
}

// A playlist page also has a section continuation for endlessly refreshed
// suggestions. Follow only the track shelf when it is present, even when
// that shelf has no next page. Library grids still use the generic walker.
func browseContents(root map[string]any) map[string]any {
	var shelf func(any) (any, bool)
	shelf = func(value any) (any, bool) {
		switch v := value.(type) {
		case map[string]any:
			for _, key := range []string{"musicPlaylistShelfRenderer", "musicPlaylistShelfContinuation", "playlistVideoListRenderer", "playlistVideoListContinuation"} {
				if content, ok := v[key].(map[string]any); ok {
					return content, true
				}
			}
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if content, ok := shelf(v[key]); ok {
					return content, true
				}
			}
		case []any:
			for _, item := range v {
				if content, ok := shelf(item); ok {
					return content, true
				}
			}
		}
		return nil, false
	}
	if content, ok := shelf(root); ok {
		return content.(map[string]any)
	}
	return root
}

func (c *Catalog) browsePages(ctx context.Context, id string, accept func(map[string]any)) error {
	epoch := c.authEpoch.Load()
	auth := c.credentials()
	body := map[string]any{"browseId": id}
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := c.requestWithAuth(ctx, "browse", body, auth)
		if err != nil {
			return err
		}
		if c.authEpoch.Load() != epoch {
			return errors.New("аккаунт изменился; повторите загрузку")
		}
		content := browseContents(page)
		accept(content)
		token := continuationToken(content)
		if token == "" {
			return nil
		}
		if seen[token] {
			return errors.New("YouTube Music повторил страницу плейлиста; повторите загрузку")
		}
		seen[token] = true
		body = map[string]any{"continuation": token}
	}
}

func (c *Catalog) recommendations(ctx context.Context) ([]Track, error) {
	if err := c.requireAuth(ctx); err != nil {
		return nil, err
	}
	if c.credentials().isTV() {
		return c.tvRecommendations(ctx)
	}
	data, err := c.request(ctx, "browse", map[string]any{"browseId": "FEmusic_home"})
	if err != nil {
		return nil, err
	}
	// Personalized mixes have RDTMAK/RDAMPL ids. Prefer them over editorial shelves.
	var mix string
	var walk func(any)
	walk = func(x any) {
		if mix != "" {
			return
		}
		switch v := x.(type) {
		case map[string]any:
			if id, ok := v["playlistId"].(string); ok && (strings.HasPrefix(id, "RDTMAK") || strings.HasPrefix(id, "RDAMPL")) {
				mix = id
				return
			}
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(v[key])
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(data)
	if mix != "" {
		data, err = c.request(ctx, "next", map[string]any{"playlistId": mix, "isAudioOnly": true})
		if err != nil {
			return nil, err
		}
	}
	tracks := parseTracks(data)
	if len(tracks) == 0 {
		return nil, errors.New("YouTube Music не вернул рекомендации для этого профиля")
	}
	return tracks, nil
}
