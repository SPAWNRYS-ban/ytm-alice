package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const version = "0.8.0-luna.1"
const relayAddr = "127.0.0.1:18883"

func dataDir() string {
	if s := os.Getenv("YTM_ALICE_DATA"); s != "" {
		return s
	}
	if s := os.Getenv("YAM_VAR"); s != "" {
		return filepath.Join(s, "local.ytm-alice")
	}
	root := os.Getenv("YAM_ROOT")
	if root == "" {
		root = "/data/yam"
	}
	return filepath.Join(root, "var", "local.ytm-alice")
}
func runDir() string     { return "/tmp/ytm-alice-run" }
func socketPath() string { return filepath.Join(runDir(), "control.sock") }

type State struct {
	Version     string      `json:"version"`
	Status      string      `json:"status"`
	Queue       []Track     `json:"queue"`
	Index       int         `json:"index"`
	PositionMS  int64       `json:"position_ms"`
	Error       string      `json:"error,omitempty"`
	Announce    bool        `json:"announce"`
	LoggedIn    bool        `json:"logged_in"`
	NativeMedia bool        `json:"native_media"`
	Auth        *AuthStatus `json:"auth,omitempty"`
}
type request struct {
	Action     string       `json:"action"`
	Query      string       `json:"query,omitempty"`
	Index      int          `json:"index,omitempty"`
	PositionMS int64        `json:"position_ms,omitempty"`
	Auth       *Credentials `json:"auth,omitempty"`
}
type response struct {
	State     *State           `json:"state,omitempty"`
	Tracks    []Track          `json:"tracks,omitempty"`
	Error     string           `json:"error,omitempty"`
	Auth      *AuthStatus      `json:"auth,omitempty"`
	Playlists []Playlist       `json:"playlists,omitempty"`
	Accounts  []YouTubeAccount `json:"accounts,omitempty"`
}
type Server struct {
	mu             sync.Mutex
	state          State
	catalog        *Catalog
	player         *nativePlayer
	generation     uint64
	prepCancel     context.CancelFunc
	latestSearch   []Track
	speechMu       sync.Mutex
	closing        bool
	mediaConnected atomic.Bool
	luna           *lunaClient
	retiring       []*nativePlayer
	coordinatorMu  sync.Mutex
	intentMu       sync.Mutex
	intentCancel   context.CancelFunc
	intent         uint64
}

func (s *Server) snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.state
	v.Queue = append([]Track(nil), v.Queue...)
	for i := range v.Queue {
		if v.Queue[i].ThumbnailURL == "" {
			v.Queue[i].ThumbnailURL = fallbackThumbnail(v.Queue[i].ID)
		}
	}
	v.LoggedIn = s.catalog != nil && s.catalog.credentials() != nil
	v.NativeMedia = s.mediaConnected.Load()
	if s.catalog != nil {
		status := s.catalog.authStatus()
		v.Auth = &status
	}
	return v
}
func (s *Server) saveLocked() {
	raw, _ := json.Marshal(s.state)
	path := filepath.Join(dataDir(), "state.json")
	if err := os.WriteFile(path+".tmp", raw, 0600); err == nil {
		os.Rename(path+".tmp", path)
	}
}
func (s *Server) execute(ctx context.Context, r request) response {
	switch r.Action {
	case "auth-status", "auth-check", "auth-import", "auth-logout", "auth-discover", "auth-accounts", "auth-select":
		return s.authCommand(ctx, r)
	case "playlists":
		playlists, err := s.catalog.library(ctx)
		if err != nil {
			return s.catalogError(err)
		}
		return response{Playlists: playlists}
	case "playlist":
		if err := s.catalog.requireAuth(ctx); err != nil {
			return s.catalogError(err)
		}
		if !playlistID.MatchString(r.Query) {
			return response{Error: "нужен ID плейлиста из команды playlists"}
		}
		return s.command(ctx, request{Action: "play", Query: "https://music.youtube.com/playlist?list=" + r.Query})
	case "status", "queue":
		v := s.snapshot()
		return response{State: &v}
	case "search", "liked":
		epoch := s.catalog.authEpoch.Load()
		query := r.Query
		if r.Action == "liked" {
			query = "мои лайки"
		}
		t, e := s.catalog.lookup(ctx, query)
		if e != nil {
			if r.Action == "liked" && errors.Is(e, errNoTracks) {
				return response{Tracks: []Track{}}
			}
			return s.catalogError(e)
		}
		s.mu.Lock()
		if epoch != s.catalog.authEpoch.Load() {
			s.mu.Unlock()
			return response{Error: "аккаунт изменился; повторите поиск"}
		}
		s.latestSearch = append([]Track(nil), t...)
		s.mu.Unlock()
		return response{Tracks: t}
	case "play":
		s.mu.Lock()
		s.stopLocked()
		s.state.Status = "searching"
		s.state.Error = ""
		g := s.generation
		s.mu.Unlock()
		tracks, e := s.catalog.lookup(ctx, r.Query)
		s.mu.Lock()
		if s.generation != g {
			s.mu.Unlock()
			return response{Error: "запрос заменён более новым"}
		}
		if e != nil {
			s.state.Status = "error"
			s.state.Error = e.Error()
			s.mu.Unlock()
			return response{Error: e.Error()}
		}
		s.state.Queue = tracks
		s.state.Index = 0
		s.state.PositionMS = 0
		s.saveLocked()
		s.mu.Unlock()
		s.start(0, true)
		if !isLikedQuery(r.Query) && !strings.Contains(r.Query, "://") && !strings.HasPrefix(strings.ToLower(r.Query), "альбом ") && !strings.HasPrefix(strings.ToLower(r.Query), "плейлист ") {
			go s.fillRadio(g+1, tracks[0].ID)
		}
	case "select", "jump":
		s.mu.Lock()
		i := r.Index - 1
		tracks := s.latestSearch
		message := "номер вне результатов поиска"
		if r.Action == "jump" {
			tracks = s.state.Queue
			message = "номер вне очереди"
		}
		if i < 0 || i >= len(tracks) {
			s.mu.Unlock()
			return response{Error: message}
		}
		if r.Query != "" && r.Query != tracks[i].ID {
			s.mu.Unlock()
			return response{Error: "список изменился; обновите его и выберите трек снова"}
		}
		s.state.Queue = append([]Track(nil), tracks...)
		s.state.Index = i
		s.state.PositionMS = 0
		s.mu.Unlock()
		s.start(0, true)
	case "next", "previous", "resume", "pause", "toggle", "stop", "seek", "announce-on", "announce-off":
		s.mu.Lock()
		if r.Action == "toggle" {
			r.Action = "resume"
			if s.state.Status == "playing" || s.state.Status == "buffering" || s.state.Status == "searching" {
				r.Action = "pause"
			}
		}
		switch r.Action {
		case "announce-on", "announce-off":
			s.state.Announce = r.Action == "announce-on"
		case "pause":
			s.mu.Unlock()
			if err := s.setPlayback(ctx, "paused"); err != nil {
				return response{Error: err.Error()}
			}
			return response{State: ptrState(s.snapshot())}
		case "resume":
			s.mu.Unlock()
			if err := s.setPlayback(ctx, "playing"); err != nil {
				return response{Error: err.Error()}
			}
			return response{State: ptrState(s.snapshot())}
		case "stop":
			s.stopLocked()
			s.state.Status = "stopped"
			s.state.PositionMS = 0
			s.state.Error = ""
		case "seek":
			s.mu.Unlock()
			return response{Error: "Штатный плеер этой прошивки не поддерживает надёжную перемотку"}
		case "next", "previous":
			step := 1
			if r.Action == "previous" {
				step = -1
			}
			i := s.state.Index + step
			if i < 0 || i >= len(s.state.Queue) {
				s.mu.Unlock()
				return response{Error: "достигнут край очереди"}
			}
			s.state.Index = i
			s.state.PositionMS = 0
			s.mu.Unlock()
			// Keep native TTS outside Luna's bounded transport operation.
			s.start(0, s.luna == nil)
			v := s.snapshot()
			return response{State: &v}
		}
		s.saveLocked()
		s.mu.Unlock()
	case "voice":
		a, q, ok := voiceAction(r.Query)
		if !ok {
			return response{Error: "нет команды YouTube Music"}
		}
		if a == "what" {
			v := s.snapshot()
			if len(v.Queue) > 0 {
				go func() {
					s.speechMu.Lock()
					defer s.speechMu.Unlock()
					t := v.Queue[v.Index]
					if err := speakOwned(t.Artists + ". " + t.Title); err != nil {
						log.Print("track announcement not confirmed: ", err)
					}
				}()
			}
			return response{State: &v}
		}
		if err := cancelDialog(); err != nil {
			return response{Error: "не удалось завершить диалог Алисы"}
		}
		time.Sleep(120 * time.Millisecond)
		return s.command(ctx, request{Action: a, Query: q})
	default:
		return response{Error: "неизвестная команда"}
	}
	v := s.snapshot()
	return response{State: &v}
}
func (s *Server) fillRadio(g uint64, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tracks := s.catalog.radio(ctx, id)
	if len(tracks) < 2 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != g || len(s.state.Queue) == 0 || s.state.Queue[0].ID != id {
		return
	}
	seen := map[string]bool{}
	out := append([]Track(nil), s.state.Queue[:s.state.Index+1]...)
	for _, t := range out {
		seen[t.ID] = true
	}
	for _, t := range tracks {
		if !seen[t.ID] {
			out = append(out, t)
			seen[t.ID] = true
		}
	}
	if len(out) > 1 {
		s.state.Queue = out
		s.saveLocked()
	}
}
func (s *Server) relay(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	s.mu.Lock()
	p := s.player
	if p == nil || r.URL.Path != "/audio/"+p.token {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	src := p.source
	s.mu.Unlock()
	if r.Method == "HEAD" {
		w.Header().Set("Content-Type", src.mime)
		w.Header().Set("Accept-Ranges", "bytes")
		if src.length > 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(src.length, 10))
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	start, end, err := byteRange(r.Header.Get("Range"), src.length)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", src.length))
		w.WriteHeader(416)
		return
	}
	s.relayAudio(w, r, p, src, start, end)
}

func byteRange(header string, length int64) (int64, int64, error) {
	if length <= 0 {
		return 0, 0, errors.New("unknown length")
	}
	if header == "" {
		return 0, length - 1, nil
	}
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, errors.New("invalid range")
	}
	pair := strings.SplitN(strings.TrimPrefix(header, "bytes="), "-", 2)
	if len(pair) != 2 {
		return 0, 0, errors.New("invalid range")
	}
	start, end := int64(0), length-1
	var err error
	if pair[0] == "" {
		n, e := strconv.ParseInt(pair[1], 10, 64)
		if e != nil || n <= 0 {
			return 0, 0, errors.New("invalid suffix")
		}
		start = length - n
		if start < 0 {
			start = 0
		}
	} else {
		start, err = strconv.ParseInt(pair[0], 10, 64)
		if err != nil || start < 0 || start >= length {
			return 0, 0, errors.New("invalid start")
		}
		if pair[1] != "" {
			end, err = strconv.ParseInt(pair[1], 10, 64)
			if err != nil || end < start {
				return 0, 0, errors.New("invalid end")
			}
			if end >= length {
				end = length - 1
			}
		}
	}
	return start, end, nil
}
func newCatalog() *Catalog {
	roots, _ := x509.SystemCertPool()
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if pem, e := os.ReadFile("/system/vendor/quasar/certs/ca-certificates.crt"); e == nil {
		roots.AppendCertsFromPEM(pem)
	}
	h := &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 15 * time.Second}}
	return &Catalog{http: h, cache: map[string]catalogEntry{}, streams: map[string]streamInfo{}}
}
func call(r request) (response, error) {
	c, e := net.DialTimeout("unix", socketPath(), 2*time.Second)
	if e != nil {
		return response{}, errors.New("ytm-alice не запущен: ytm-alice daemon start")
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(50 * time.Second))
	if e = json.NewEncoder(c).Encode(r); e != nil {
		return response{}, e
	}
	var out response
	e = json.NewDecoder(io.LimitReader(c, 1<<20)).Decode(&out)
	return out, e
}
func printResponse(out response, asJSON bool) {
	if asJSON {
		json.NewEncoder(os.Stdout).Encode(out)
		return
	}
	if out.Error != "" {
		fmt.Fprintln(os.Stderr, out.Error)
		return
	}
	if out.Auth != nil {
		printAuth(out.Auth)
		return
	}
	if out.Playlists != nil {
		for i, p := range out.Playlists {
			fmt.Printf("%2d. %s\n    ytm-alice playlist %s\n", i+1, p.Title, p.ID)
		}
		return
	}
	if out.State != nil {
		s := out.State
		fmt.Printf("YouTube Music · %s · %s\n", s.Version, s.Status)
		if len(s.Queue) > 0 && s.Index >= 0 && s.Index < len(s.Queue) {
			t := s.Queue[s.Index]
			fmt.Printf("▶ %s — %s\n  %d/%d · %d:%02d / %d:%02d\n", t.Artists, t.Title, s.Index+1, len(s.Queue), s.PositionMS/60000, s.PositionMS/1000%60, t.DurationMS/60000, t.DurationMS/1000%60)
		}
		if s.Error != "" {
			fmt.Println("Ошибка:", s.Error)
		}
	} else {
		for i, t := range out.Tracks {
			fmt.Printf("%2d. %s — %s\n", i+1, t.Artists, t.Title)
		}
	}
}
func help() {
	fmt.Println("ytm-alice " + version + ` · автономный YouTube Music для Яндекс Станций

Проверено на Станции Лайт 1 (YNDX-00025, yandexmicro).
На Станции Мини 2 с часами (yandexmini_2) подтверждены установка, запуск и воспроизведение.
Работа на остальных моделях пока не проверена.

  play "запрос или ссылка"   включить трек, альбом или плейлист
  search "запрос"            поиск; select N — выбрать результат
  jump N                     включить трек из текущей очереди
  login                      импорт JSON-сессии из stdin (клиент ПК: Firefox)
  auth status | auth check   состояние / проверка входа
  logout                     удалить сессию YouTube Music
  liked | playlists          лайки / личные плейлисты
  playlist ID                включить личный плейлист
  play "мои лайки"            включить понравившиеся
  web status                 адреса локальной HTTP-панели
  status | queue             состояние и очередь
  pause | resume | toggle | stop  управление воспроизведением
  next | previous            следующий / предыдущий трек
  announce-on | announce-off  фраза Алисы «Включаю»
  ui                         интерактивное меню
  --json КОМАНДА              машиночитаемый ответ

Голосом: «Алиса, включи на ютубе …».
Без аргументов открывается меню; фоновый режим — serve.
Автозапуск: ytm-alice daemon install.`)
}
func ui() error {
	reader := bufio.NewScanner(os.Stdin)
	fmt.Println("ytm-alice · / запрос — поиск, p запрос — включить, номер — выбрать")
	fmt.Println("n/b — дальше/назад, space — пауза/продолжить, s — стоп, q — выход")
	for {
		out, e := call(request{Action: "status"})
		if e != nil {
			return e
		}
		printResponse(out, false)
		fmt.Print("ytm> ")
		if !reader.Scan() {
			return reader.Err()
		}
		line := reader.Text()
		trim := strings.TrimSpace(line)
		var r request
		switch {
		case trim == "q":
			return nil
		case strings.HasPrefix(trim, "/"):
			r = request{Action: "search", Query: strings.TrimSpace(trim[1:])}
		case strings.HasPrefix(trim, "p "):
			r = request{Action: "play", Query: strings.TrimSpace(trim[2:])}
		case trim == "n":
			r.Action = "next"
		case trim == "b":
			r.Action = "previous"
		case trim == "s":
			r.Action = "stop"
		case trim == "":
			r.Action = "pause"
			if out.State != nil && out.State.Status == "paused" {
				r.Action = "resume"
			}
		default:
			n, e := strconv.Atoi(trim)
			if e != nil {
				continue
			}
			r = request{Action: "select", Index: n}
		}
		out, e = call(r)
		if e != nil {
			return e
		}
		printResponse(out, false)
	}
}
func main() {
	args := os.Args[1:]
	asJSON := false
	for i := 0; i < len(args); {
		if args[i] == "--json" {
			asJSON = true
			args = append(args[:i], args[i+1:]...)
		} else {
			i++
		}
	}
	if len(args) == 0 {
		if info, e := os.Stdin.Stat(); e == nil && info.Mode()&os.ModeCharDevice != 0 && !asJSON {
			args = []string{"ui"}
		} else {
			args = []string{"status"}
		}
	}
	if args[0] == "serve" {
		if e := serve(); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	switch args[0] {
	case "web":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "web status")
			os.Exit(2)
		}
		if err := webCLI(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case "auth":
		if len(args) != 2 || (args[1] != "status" && args[1] != "check") {
			fmt.Fprintln(os.Stderr, "auth status|check")
			os.Exit(2)
		}
		args = []string{"auth-" + args[1]}
	case "logout":
		args = []string{"auth-logout"}
	case "login":
		var auth Credentials
		if json.NewDecoder(io.LimitReader(os.Stdin, maxControlRequestBytes)).Decode(&auth) != nil {
			fmt.Fprintln(os.Stderr, "нужен JSON сессии из stdin; на ПК: ytm-alice login")
			os.Exit(2)
		}
		out, err := call(request{Action: "auth-import", Auth: &auth})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		printResponse(out, asJSON)
		if out.Error != "" {
			os.Exit(1)
		}
		return
	}
	switch args[0] {
	case "--rpc", "rpc":
		if e := rpcLoop(os.Stdin, os.Stdout, call); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	case "daemon":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "daemon start|stop|restart|install|uninstall")
			os.Exit(2)
		}
		if e := daemon(args[1]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	case "--help", "-h", "help":
		help()
		return
	case "--version", "version":
		fmt.Println(version)
		return
	case "say":
		if e := speak(strings.Join(args[1:], " ")); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		fmt.Println("Native TTS completed")
		return
	case "ui":
		if e := ui(); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	case "probe":
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		c := newCatalog()
		t, e := c.lookup(ctx, strings.Join(args[1:], " "))
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		fmt.Println("Found:", len(t), t[0].Title)
		src, e := c.resolve(ctx, t[0].ID)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		for _, offset := range []int64{0, src.length / 2} {
			end := offset + 65535
			if end >= src.length {
				end = src.length - 1
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", src.url, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
			resp, err := c.http.Do(req)
			if err != nil {
				fmt.Fprintln(os.Stderr, "stream network error")
				os.Exit(1)
			}
			n, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
			resp.Body.Close()
			fmt.Println("Stream offset", offset, "HTTP", resp.StatusCode, "bytes", n)
			if resp.StatusCode != 206 || readErr != nil || n != end-offset+1 {
				os.Exit(1)
			}
		}
		return
	}
	action := args[0]
	switch action {
	case "p":
		action = "play"
	case "n":
		action = "next"
	case "b":
		action = "previous"
	case "s":
		action = "status"
	case "q":
		action = "queue"
	}
	r := request{Action: action, Query: strings.Join(args[1:], " ")}
	if action == "select" || action == "jump" {
		n, e := strconv.Atoi(r.Query)
		if e != nil || n < 1 {
			fmt.Fprintln(os.Stderr, "нужен номер трека, начиная с 1")
			os.Exit(2)
		}
		r.Index = n
	}
	if action == "seek" {
		v, e := strconv.ParseFloat(r.Query, 64)
		if e != nil || v < 0 || v > 7200 {
			fmt.Fprintln(os.Stderr, "нужна позиция в секундах")
			os.Exit(2)
		}
		r.PositionMS = int64(v * 1000)
	}
	out, e := call(r)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if action == "queue" && out.State != nil {
		out.Tracks = out.State.Queue
		if !asJSON {
			out.State = nil
		}
	}
	printResponse(out, asJSON)
	if out.Error != "" {
		os.Exit(1)
	}
}
