package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	yt "github.com/kkdai/youtube/v2"
)

// unavailableTrack marks a permanent restriction on one track, rather than a
// network failure or rate limit. Only this kind can advance the queue.
type unavailableTrack struct{ message string }

func (e unavailableTrack) Error() string { return e.message }

// Some Music tracks require the explicit-content acknowledgement and a browser
// session. Keep that session on the existing, exact music.youtube.com endpoint;
// the public resolver, JS downloads and signed CDN URLs remain credential-free.
func (c *Catalog) musicVideo(ctx context.Context, id string) (*yt.Video, error) {
	playback := map[string]any{"html5Preference": "HTML5_PREF_WANTS"}
	// The signature must match the player program that decodes it: without
	// the timestamp YouTube may sign for another player version, and the CDN
	// answers 403 to every URL decoded with ours.
	if sts := c.signatureTimestamp(ctx, id); sts > 0 {
		playback["signatureTimestamp"] = sts
	}
	data, err := c.request(ctx, "player", map[string]any{"videoId": id, "contentCheckOk": true, "racyCheckOk": true, "playbackContext": map[string]any{"contentPlaybackContext": playback}})
	if err != nil {
		return nil, err
	}
	status := str(get(data, "playabilityStatus", "status"))
	switch status {
	case "OK":
	case "LOGIN_REQUIRED":
		return nil, unavailableTrack{"YouTube: для этого трека нужен вход и подтверждённый возраст аккаунта; подключите или обновите cookies в настройках"}
	case "CONTENT_CHECK_REQUIRED", "AGE_CHECK_REQUIRED":
		return nil, unavailableTrack{"YouTube: подтвердите доступ к этому треку в YouTube Music и обновите cookies"}
	case "UNPLAYABLE", "ERROR":
		return nil, unavailableTrack{"YouTube: трек недоступен для этого аккаунта или региона"}
	default:
		return nil, errors.New("YouTube: не удалось получить аудиопоток")
	}
	var formats struct {
		Adaptive []yt.Format `json:"adaptiveFormats"`
		Formats  []yt.Format `json:"formats"`
	}
	raw, _ := json.Marshal(get(data, "streamingData"))
	if json.Unmarshal(raw, &formats) != nil {
		return nil, errors.New("YouTube: некорректное описание аудиопотока")
	}
	seconds, _ := strconv.ParseInt(str(get(data, "videoDetails", "lengthSeconds")), 10, 64)
	return &yt.Video{ID: id, Title: str(get(data, "videoDetails", "title")), Author: str(get(data, "videoDetails", "author")), Duration: time.Duration(seconds) * time.Second, Formats: append(formats.Adaptive, formats.Formats...)}, nil
}

func resolverError(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("загрузка трека отменена")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("YouTube: истекло время загрузки трека")
	}
	if errors.Is(err, yt.ErrLoginRequired) {
		return errors.New("YouTube: для этого трека нужен вход и подтверждённый возраст аккаунта")
	}
	if errors.Is(err, yt.ErrVideoPrivate) {
		return errors.New("YouTube: доступ к этому треку ограничен владельцем")
	}
	var status *yt.ErrPlayabiltyStatus
	if errors.As(err, &status) {
		return fmt.Errorf("YouTube: трек недоступен (%s)", status.Status)
	}
	return errors.New("YouTube: не удалось получить аудиопоток")
}

var playerTimestampPattern = regexp.MustCompile(`(?:signatureTimestamp|sts)\s*:\s*(\d{5})`)

// playerTimestamp reads the signature timestamp of a player program; 0 when
// it is missing.
func playerTimestamp(player string) int {
	m := playerTimestampPattern.FindStringSubmatch(player)
	if m == nil {
		return 0
	}
	sts, _ := strconv.Atoi(m[1])
	return sts
}

func (c *Catalog) setSignatureTimestamp(sts int) {
	c.stsMu.Lock()
	c.playerSTS = sts
	c.stsMu.Unlock()
}

// signatureTimestamp returns the timestamp of the current player program,
// loading the program once when no decoding has needed it yet. A failed load
// returns 0 and the request goes without it, as before.
func (c *Catalog) signatureTimestamp(ctx context.Context, id string) int {
	c.stsMu.Lock()
	defer c.stsMu.Unlock()
	if c.playerSTS > 0 {
		return c.playerSTS
	}
	player, err := c.playerProgram(ctx, id)
	if err != nil {
		return 0
	}
	c.playerSTS = playerTimestamp(player)
	return c.playerSTS
}
