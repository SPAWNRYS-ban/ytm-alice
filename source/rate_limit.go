package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	yt "github.com/kkdai/youtube/v2"
)

// The limit belongs to the IP, across public and authenticated player clients.
type rateLimited struct{ until time.Time }

func (e rateLimited) Error() string {
	minutes := max(1, int(time.Until(e.until).Minutes())+1)
	return fmt.Sprintf("YouTube временно ограничил запросы с этого IP (429); повторите через %d мин", minutes)
}
func (c *Catalog) playbackLimit() error {
	c.limitMu.Lock()
	defer c.limitMu.Unlock()
	if time.Now().Before(c.limitedUntil) {
		return rateLimited{c.limitedUntil}
	}
	return nil
}
func (c *Catalog) noteRateLimit() error {
	c.limitMu.Lock()
	defer c.limitMu.Unlock()
	c.limitBackoff = min(max(2*c.limitBackoff, 2*time.Minute), 30*time.Minute)
	c.limitedUntil = time.Now().Add(c.limitBackoff)
	return rateLimited{c.limitedUntil}
}
func (c *Catalog) clearRateLimit() {
	c.limitMu.Lock()
	defer c.limitMu.Unlock()
	// Another in-flight metadata request may just have observed a 429.
	if time.Now().Before(c.limitedUntil) {
		return
	}
	c.limitBackoff = 0
	c.limitedUntil = time.Time{}
}
func (c *Catalog) videoInfo(ctx context.Context, client *yt.Client, id string) (*yt.Video, error) {
	if err := c.playbackLimit(); err != nil {
		return nil, err
	}
	return client.GetVideoContext(ctx, id)
}

func (c *Catalog) trackVideo(ctx context.Context, id string) (*yt.Video, error) {
	if err := c.playbackLimit(); err != nil {
		return nil, err
	}
	var v *yt.Video
	var musicErr error
	// The Music player endpoint needs browser cookies; a TV token is not accepted there.
	if auth := c.credentials(); auth != nil && auth.Cookie != "" {
		v, musicErr = c.musicVideo(ctx, id)
		if musicErr == nil {
			return v, nil
		}
		var limited rateLimited
		if errors.As(musicErr, &limited) {
			return nil, musicErr
		}
	}
	err := c.withYT(func(client *yt.Client) (err error) { v, err = c.videoInfo(ctx, client, id); return err })
	if err == nil {
		return v, nil
	}
	var limited rateLimited
	if errors.As(err, &limited) {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, resolverError(ctx.Err())
	}
	if musicErr != nil {
		return nil, musicErr
	}
	return c.musicVideo(ctx, id)
}

// RoundTrip cannot log wrapped network errors: they may include signed URLs.
func (p *ytPageCache) network(req *http.Request) (*http.Response, error) {
	if p.catalog != nil {
		if err := p.catalog.playbackLimit(); err != nil {
			return nil, err
		}
	}
	response, err := p.base.RoundTrip(req)
	if err == nil && response.StatusCode == http.StatusTooManyRequests && p.catalog != nil {
		response.Body.Close()
		return nil, p.catalog.noteRateLimit()
	}
	return response, err
}
