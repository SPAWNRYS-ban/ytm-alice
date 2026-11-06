package main

import (
	"context"
	"regexp"
	"strings"
)

type Playlist struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
}

var playlistID = regexp.MustCompile(`^[A-Za-z0-9_-]{2,180}$`)

func parsePlaylists(root any) []Playlist {
	result := []Playlist{}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(x any) {
		switch v := x.(type) {
		case map[string]any:
			if item, ok := v["tileRenderer"]; ok {
				if list, ok := parsePlaylistTile(item); ok && !seen[list.ID] {
					seen[list.ID] = true
					result = append(result, list)
				}
				return
			}
			for _, renderer := range []string{"musicTwoRowItemRenderer", "musicResponsiveListItemRenderer"} {
				if item, ok := v[renderer]; ok {
					id := firstKey(get(item, "navigationEndpoint"), "browseId")
					if strings.HasPrefix(id, "VL") {
						id = strings.TrimPrefix(id, "VL")
					} else {
						id = firstKey(item, "playlistId")
					}
					title := text(get(item, "title"))
					if title == "" {
						title = text(get(item, "flexColumns", 0, "musicResponsiveListItemFlexColumnRenderer", "text"))
					}
					if playlistID.MatchString(id) && title != "" && !seen[id] {
						seen[id] = true
						result = append(result, Playlist{ID: id, Title: title, ThumbnailURL: thumbnailURL(item)})
					}
				}
			}
			for _, value := range v {
				walk(value)
			}
		case []any:
			for _, value := range v {
				walk(value)
			}
		}
	}
	walk(root)

	return result
}

func (c *Catalog) library(ctx context.Context) ([]Playlist, error) {
	if err := c.requireAuth(ctx); err != nil {
		return nil, err
	}
	if c.credentials().isTV() {
		return c.tvLibrary(ctx)
	}
	lists := []Playlist{}
	seen := map[string]bool{}
	err := c.browsePages(ctx, "FEmusic_liked_playlists", func(page map[string]any) {
		for _, list := range parsePlaylists(page) {
			if !seen[list.ID] {
				seen[list.ID] = true
				lists = append(lists, list)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return lists, nil
}
