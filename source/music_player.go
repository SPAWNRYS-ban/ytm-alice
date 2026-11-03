package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	yt "github.com/kkdai/youtube/v2"
)

// Some Music tracks require the explicit-content acknowledgement and a browser
// session. Keep that session on the existing, exact music.youtube.com endpoint;
// the public resolver, JS downloads and signed CDN URLs remain credential-free.
func (c *Catalog) musicVideo(ctx context.Context, id string) (*yt.Video, error) {
	data, err := c.request(ctx, "player", map[string]any{"videoId": id, "contentCheckOk": true, "racyCheckOk": true, "playbackContext": map[string]any{"contentPlaybackContext": map[string]any{"html5Preference": "HTML5_PREF_WANTS"}}})
	if err != nil {
		return nil, err
	}
	status := str(get(data, "playabilityStatus", "status"))
	switch status {
	case "OK":
	case "LOGIN_REQUIRED":
		return nil, errors.New("YouTube: для этого трека нужен вход и подтверждённый возраст аккаунта; подключите или обновите cookies в настройках")
	case "CONTENT_CHECK_REQUIRED", "AGE_CHECK_REQUIRED":
		return nil, errors.New("YouTube: подтвердите доступ к этому треку в YouTube Music и обновите cookies")
	case "UNPLAYABLE", "ERROR":
		return nil, errors.New("YouTube: трек недоступен для этого аккаунта или региона")
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
