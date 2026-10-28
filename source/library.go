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
	if len(result) > 50 {
		result = result[:50]
	}
	return result
}

func (c *Catalog) library(ctx context.Context) ([]Playlist, error) {
	if err := c.requireAuth(ctx); err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "browse", map[string]any{"browseId": "FEmusic_liked_playlists"})
	if err != nil {
		return nil, err
	}
	return parsePlaylists(data), nil
}
