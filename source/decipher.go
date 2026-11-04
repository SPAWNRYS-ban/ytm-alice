package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"time"

	yt "github.com/kkdai/youtube/v2"
)

// yt-dlp/ejs 0.8.0; includes its dependency license notices.
//
//go:embed resolver/solver.js
var decipherScript []byte

var playerScriptPath = regexp.MustCompile(`/s/player/[A-Za-z0-9_-]+/[A-Za-z0-9._/-]*/base\.js`)

func (c *Catalog) playerProgram(ctx context.Context, id string) (string, error) {
	var program string
	err := c.withYT(func(client *yt.Client) (err error) {
		program, err = readPlayerProgram(ctx, id, client.HTTPClient)
		return err
	})
	return program, err
}
func readPlayerProgram(ctx context.Context, id string, h *http.Client) (string, error) {
	client := *h
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) > 4 || r.URL.Scheme != "https" || (r.URL.Host != "www.youtube.com" && r.URL.Host != "youtube.com") {
			return http.ErrUseLastResponse
		}
		return nil
	}
	read := func(target string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", yt.DefaultClient.UserAgent)
		req.Header.Set("Origin", "https://youtube.com")
		req.AddCookie(&http.Cookie{Name: "CONSENT", Value: "YES+cb.20210328-17-p0.en+FX+999"})
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return nil, errors.New("player HTTP error")
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		if err != nil || int64(len(data)) > limit {
			return nil, errors.New("player response too large")
		}
		return data, nil
	}
	html, err := read("https://youtube.com/embed/"+id+"?hl=en", 2<<20)
	if err != nil {
		return "", err
	}
	path := string(playerScriptPath.Find(html))
	if path == "" {
		return "", errors.New("player script missing")
	}
	data, err := read("https://www.youtube.com"+path, 8<<20)
	return string(data), err
}

func runDecipher(ctx context.Context, input any) (map[string]any, error) {
	path := os.Getenv("YTM_ALICE_RESOLVER")
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(filepath.Dir(exe), "ytm-alice-resolver")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	frame := func(data []byte) io.Reader {
		header := make([]byte, 4)
		binary.LittleEndian.PutUint32(header, uint32(len(data)))
		return io.MultiReader(bytes.NewReader(header), bytes.NewReader(data))
	}
	debug.FreeOSMemory()
	ctx, cancel := context.WithTimeout(ctx, 28*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = []string{"LANG=C"}
	cmd.Stdin = io.MultiReader(frame(decipherScript), frame(raw))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, errors.New("помощник декодирования не установлен; обновите пакет ytm-alice")
	}
	result, readErr := io.ReadAll(io.LimitReader(out, (12<<20)+1))
	if readErr != nil || len(result) > 12<<20 {
		_ = cmd.Process.Kill()
	}
	err = cmd.Wait()
	if err != nil || readErr != nil || len(result) > 12<<20 {
		return nil, errors.New("не удалось декодировать аудиопоток в пределах ресурсов колонки")
	}
	var response map[string]any
	if json.Unmarshal(result, &response) != nil || str(response["type"]) != "result" {
		return nil, errors.New("YouTube изменил формат подписи аудиопотока")
	}
	return response, nil
}

// Caller holds c.mu, serializing the memory-bounded native helper and its cache.
func (c *Catalog) decipherURL(ctx context.Context, id string, format yt.Format) (string, error) {
	link, sig, sp := format.URL, "", "signature"
	if link == "" {
		params, err := url.ParseQuery(format.Cipher)
		if err != nil {
			return "", err
		}
		link, sig = params.Get("url"), params.Get("s")
		if params.Get("sp") != "" {
			sp = params.Get("sp")
		}
	}
	u, err := url.Parse(link)
	if err != nil || !validAudioURL(u) {
		return "", errors.New("некорректный адрес аудиопотока")
	}
	query := u.Query()
	n := query.Get("n")
	requests := []any{}
	if sig != "" {
		requests = append(requests, map[string]any{"type": "sig", "challenges": []string{sig}})
	}
	if n != "" {
		requests = append(requests, map[string]any{"type": "n", "challenges": []string{n}})
	}
	if len(requests) == 0 {
		return u.String(), nil
	}
	if c.playerSource == "" {
		player, err := c.playerProgram(ctx, id)
		if err != nil {
			var limited rateLimited
			if errors.As(err, &limited) {
				return "", err
			}
			return "", errors.New("не удалось загрузить декодер YouTube")
		}
		program, err := preprocessPlayer(player)
		if err != nil {
			return "", err
		}
		c.playerSource = program
	}
	input := map[string]any{"type": "preprocessed", "preprocessed_player": c.playerSource, "requests": requests}
	result, err := runDecipher(ctx, input)
	if err != nil {
		c.playerSource = ""
		return "", err
	}
	responses, ok := result["responses"].([]any)
	if !ok || len(responses) != len(requests) {
		return "", errors.New("неполный ответ декодера YouTube")
	}
	for i, response := range responses {
		challenge := sig
		key := sp
		if str(requests[i].(map[string]any)["type"]) == "n" {
			challenge = n
			key = "n"
		}
		decoded := str(get(response, "data", challenge))
		if str(get(response, "type")) != "result" || decoded == "" {
			c.playerSource = ""
			return "", errors.New("YouTube изменил формат подписи аудиопотока")
		}
		query.Set(key, decoded)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func validAudioURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && u.Port() == "" && len(u.Hostname()) > len(".googlevideo.com") && u.Hostname()[len(u.Hostname())-len(".googlevideo.com"):] == ".googlevideo.com"
}
