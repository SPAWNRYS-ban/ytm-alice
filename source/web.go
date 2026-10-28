package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web/*
var webAssets embed.FS

func localAddresses() []net.IP {
	result := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	addresses, _ := net.InterfaceAddrs()
	for _, a := range addresses {
		ip, _, err := net.ParseCIDR(a.String())
		if err == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() {
			result = append(result, ip)
		}
	}
	return result
}
func webCLI(action string) error {
	if action != "status" {
		return errors.New("web status")
	}
	fmt.Println("Панель: http://alice.lan")
	for _, ip := range localAddresses() {
		if ip.To4() != nil && !ip.IsLoopback() {
			fmt.Printf("По IP: http://%s\n", ip)
		}
	}
	return nil
}

type webPanel struct {
	server *Server
	hosts  map[string]bool
}

func newWebPanel(s *Server) *webPanel {
	hosts := map[string]bool{"alice.lan": true, "alice.local": true, "localhost": true}
	for _, ip := range localAddresses() {
		hosts[ip.String()] = true
	}
	return &webPanel{server: s, hosts: hosts}
}
func hostName(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}
func (p *webPanel) originOK(r *http.Request) bool {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return r.Header.Get("Origin") == scheme+"://"+r.Host && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
func webJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func webError(w http.ResponseWriter, status int, message string) {
	webJSON(w, status, map[string]string{"error": message})
}
func webDecode(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		webError(w, 415, "нужен JSON-запрос")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxControlRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		webError(w, 400, "некорректный или слишком большой запрос")
		return false
	}
	return true
}
func (p *webPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https://*.ytimg.com https://*.googleusercontent.com https://*.ggpht.com; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("Cache-Control", "no-store")
	if !p.hosts[hostName(r.Host)] {
		webError(w, 403, "неизвестный адрес колонки")
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		webError(w, 405, "метод не поддерживается")
		return
	}
	if r.Method == "POST" && !p.originOK(r) {
		webError(w, 403, "запрос разрешён только из панели колонки")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" {
			webError(w, 405, "метод не поддерживается")
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		// A small explicit asset set; never exposes package data or credentials.
		allowed := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "cookies.mjs": "text/javascript; charset=utf-8", "icons.mjs": "text/javascript; charset=utf-8", "icons.svg": "image/svg+xml", "style.css": "text/css; charset=utf-8", "placeholder.svg": "image/svg+xml", "logo.svg": "image/svg+xml"}
		mime, ok := allowed[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(webAssets, "web/"+name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Write(data)
		return
	}
	if r.URL.Path == "/api/info" && r.Method == "GET" {
		webJSON(w, 200, map[string]any{"version": version})
		return
	}
	if r.Method == "POST" && r.Header.Get("X-Ytm-Client") != "web" {
		webError(w, 403, "запрос разрешён только из панели колонки")
		return
	}
	if r.URL.Path == "/api/state" && r.Method == "GET" {
		webJSON(w, 200, response{State: ptrState(p.server.snapshot())})
		return
	}
	if r.URL.Path != "/api/command" || r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	var command request
	if !webDecode(w, r, &command) {
		return
	}
	allowed := map[string]bool{"status": true, "queue": true, "search": true, "liked": true, "play": true, "select": true, "jump": true, "playlists": true, "playlist": true, "next": true, "previous": true, "pause": true, "resume": true, "toggle": true, "stop": true, "announce-on": true, "announce-off": true, "auth-status": true, "auth-check": true, "auth-import": true, "auth-discover": true, "auth-accounts": true, "auth-select": true, "auth-logout": true}
	if !allowed[command.Action] || len([]rune(command.Query)) > 500 {
		webError(w, 400, "команда панели не поддерживается")
		return
	}
	if command.Auth != nil && command.Action != "auth-import" && command.Action != "auth-discover" {
		webError(w, 400, "сессия разрешена только для подключения аккаунта")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	result := p.server.command(ctx, command)
	status := 200
	if result.Error != "" {
		status = 422
	}
	webJSON(w, status, result)
}
func ptrState(s State) *State { return &s }
func (s *Server) startWeb() (func(), error) {
	address := os.Getenv("YTM_ALICE_WEB_ADDR")
	if address == "off" {
		return func() {}, nil
	}
	if address == "" {
		address = ":80"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: newWebPanel(s), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 20 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print("web listener stopped: ", err)
		}
	}()
	log.Print("web panel ready; HTTP ", address)
	return func() { server.Close() }, nil
}
