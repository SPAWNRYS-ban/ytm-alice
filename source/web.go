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
	address := defaultWebAddr
	if out, err := call(request{Action: "config-get"}); err == nil && out.WebAddr != "" {
		address = out.WebAddr
	}
	if address == "off" {
		fmt.Println("Веб-панель отключена")
		return nil
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	suffix := ":" + port
	if port == "80" {
		suffix = ""
	}
	fmt.Println("Панель: http://alice.lan" + suffix)
	for _, ip := range localAddresses() {
		if ip.To4() != nil && !ip.IsLoopback() && (host == "" || host == "0.0.0.0" || host == ip.String()) {
			fmt.Printf("По IP: http://%s%s\n", ip, suffix)
		}
	}
	return nil
}

type webPanel struct {
	server *Server
}

func newWebPanel(s *Server) *webPanel {
	return &webPanel{server: s}
}
func hostName(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}

// Explicit names preserve the Host header through a reverse proxy without
// accepting arbitrary DNS-rebinding hosts or trusting forwarded headers.
func validWebHost(host string) bool {
	if len(host) == 0 || len(host) > 253 || host != strings.ToLower(host) || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}
func (p *webPanel) configuredHost(host string) bool {
	for _, allowed := range p.server.getSettings().WebHosts {
		if host == allowed {
			return true
		}
	}
	return false
}
func (p *webPanel) hostOK(r *http.Request) bool {
	host := hostName(r.Host)
	if p.configuredHost(host) {
		return true
	}
	switch host {
	case "alice.lan", "alice.local", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	// The accepted connection carries the current destination address even
	// when DHCP completed after startup or the network address has changed.
	if local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok && local.IP.Equal(ip) {
		return true
	}
	// Also allow another currently assigned interface address (for example
	// when a local reverse proxy preserves the original numeric Host).
	for _, local := range localAddresses() {
		if local.Equal(ip) {
			return true
		}
	}
	return false
}
func (p *webPanel) originOK(r *http.Request) bool {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == scheme+"://"+r.Host {
		return true
	}
	// TLS may terminate at the proxy; allow HTTPS only for explicitly named hosts.
	return p.configuredHost(hostName(r.Host)) && origin == "https://"+r.Host
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
	if !p.hostOK(r) {
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
	allowed := map[string]bool{"config-get": true, "config-set": true, "recommendations": true, "shuffle-on": true, "shuffle-off": true, "status": true, "queue": true, "search": true, "liked": true, "play": true, "select": true, "jump": true, "playlists": true, "playlist": true, "next": true, "previous": true, "pause": true, "resume": true, "toggle": true, "stop": true, "announce-on": true, "announce-off": true, "auth-status": true, "auth-check": true, "auth-import": true, "auth-discover": true, "auth-accounts": true, "auth-select": true, "auth-logout": true, "auth-tv-start": true, "auth-tv-status": true, "auth-tv-cancel": true}
	allowed["seek"] = true
	if !allowed[command.Action] || len([]rune(command.Query)) > 500 {
		webError(w, 400, "команда панели не поддерживается")
		return
	}
	if command.Settings != nil && command.Action != "config-set" {
		webError(w, 400, "настройки разрешены только для config-set")
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
	address := effectiveWebAddr(s.getSettings())
	if address == "off" {
		s.mu.Lock()
		s.webAddr = "off"
		s.mu.Unlock()
		return func() {}, nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	host, port, _ := net.SplitHostPort(address)
	if port == "0" {
		_, port, _ = net.SplitHostPort(listener.Addr().String())
		address = net.JoinHostPort(host, port)
	}
	s.mu.Lock()
	s.webAddr = address
	s.mu.Unlock()
	server := &http.Server{Handler: newWebPanel(s), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 20 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print("web listener stopped: ", err)
		}
	}()
	log.Print("web panel ready; HTTP ", address)
	return func() { server.Close() }, nil
}
