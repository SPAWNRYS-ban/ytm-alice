package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const musicOrigin = "https://music.youtube.com"

// Credentials travel in SSH stdin, the root-only socket, or the trusted local HTTP panel.
// They are never part of State, response, player arguments, or the package.
type Credentials struct {
	Cookie   string `json:"cookie"`
	AuthUser string `json:"auth_user"`
	PageID   string `json:"page_id,omitempty"`
}

func (Credentials) String() string   { return "<YouTube Music session>" }
func (Credentials) GoString() string { return "<YouTube Music session>" }

type AuthStatus struct {
	Configured bool            `json:"configured"`
	Checked    bool            `json:"checked"`
	Valid      bool            `json:"valid"`
	Expired    bool            `json:"expired,omitempty"`
	Account    *YouTubeAccount `json:"account,omitempty"`
}

func credentialPath() string { return filepath.Join(dataDir(), "youtube-session.json") }

func (a Credentials) sapisid() string {
	req := &http.Request{Header: http.Header{"Cookie": {a.Cookie}}}
	var fallback string
	for _, cookie := range req.Cookies() {
		if cookie.Name == "__Secure-3PAPISID" {
			return cookie.Value
		}
		if cookie.Name == "SAPISID" {
			fallback = cookie.Value
		}
	}
	return fallback
}

func (a *Credentials) validate() error {
	if a == nil || len(a.Cookie) == 0 || len(a.Cookie) > 48000 {
		return errors.New("нужна сессия YouTube Music до 48 КБ")
	}
	for _, ch := range a.Cookie {
		if ch < 32 || ch == 127 {
			return errors.New("некорректный Cookie: управляющие символы")
		}
	}
	if a.AuthUser == "" {
		a.AuthUser = "0"
	}
	i, err := strconv.Atoi(a.AuthUser)
	if err != nil || i < 0 || i > 99 {
		return errors.New("номер Google-аккаунта должен быть от 0 до 99")
	}
	if !validPageID(a.PageID) {
		return errors.New("некорректный профиль YouTube")
	}
	if a.sapisid() == "" {
		return errors.New("в сессии нет SAPISID; войдите в YouTube Music в браузере")
	}
	return nil
}

func (a Credentials) apply(req *http.Request, now time.Time) {
	// Do not attach credentials to video resolvers, redirects, or CDN requests.
	if req.URL.Scheme != "https" || req.URL.Host != "music.youtube.com" {
		return
	}
	stamp := strconv.FormatInt(now.Unix(), 10)
	sum := sha1.Sum([]byte(stamp + " " + a.sapisid() + " " + musicOrigin))
	req.Header.Set("Cookie", a.Cookie)
	req.Header.Set("Authorization", "SAPISIDHASH "+stamp+"_"+hex.EncodeToString(sum[:]))
	req.Header.Set("X-Goog-AuthUser", a.AuthUser)
	req.Header.Set("X-Origin", musicOrigin)
	if a.PageID != "" {
		req.Header.Set("X-Goog-PageId", a.PageID)
	}
}

func (c *Catalog) credentials() *Credentials {
	c.authMu.RLock()
	defer c.authMu.RUnlock()
	if c.auth == nil {
		return nil
	}
	copy := *c.auth
	return &copy
}

func (c *Catalog) loadAuth() error {
	f, err := os.Open(credentialPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("не удалось открыть сохранённую сессию YouTube Music")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("файл сессии должен иметь права 600 и быть обычным файлом")
	}
	var auth Credentials
	if err := json.NewDecoder(io.LimitReader(f, maxControlRequestBytes)).Decode(&auth); err != nil {
		return errors.New("повреждён файл сессии YouTube Music; выполните login снова")
	}
	if err := auth.validate(); err != nil {
		return err
	}
	c.authMu.Lock()
	c.auth = &auth
	c.authMu.Unlock()
	return nil
}

func (c *Catalog) checkAuth(ctx context.Context, auth *Credentials) error {
	account, err := c.accountInfo(ctx, auth)
	c.setAuthResult(auth, account, err)
	return err
}

func (c *Catalog) replaceAuth(ctx context.Context, auth *Credentials) error {
	c.authChange.Lock()
	defer c.authChange.Unlock()
	return c.replaceAuthLocked(ctx, auth)
}
func (c *Catalog) replaceAuthLocked(ctx context.Context, auth *Credentials) error {
	if err := auth.validate(); err != nil {
		return err
	}
	copy := *auth
	account, err := c.accountInfo(ctx, &copy)
	if err != nil {
		return err
	} // Keep the old session untouched if validation fails.
	if err := os.MkdirAll(dataDir(), 0700); err != nil {
		return errors.New("не удалось создать каталог сессии")
	}
	f, err := os.CreateTemp(dataDir(), ".youtube-session-*")
	if err != nil {
		return errors.New("не удалось сохранить сессию")
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(copy)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), credentialPath())
	}
	if err != nil {
		return errors.New("не удалось записать сессию; прежняя сохранена")
	}
	c.authMu.Lock()
	c.auth = &copy
	c.authResult = AuthStatus{Configured: true, Checked: true, Valid: true, Account: account}
	c.authCheckedAt = time.Now()
	c.authEpoch.Add(1)
	c.authMu.Unlock()
	c.clearPrivateCache()
	return nil
}

func (c *Catalog) logout() error {
	c.authChange.Lock()
	defer c.authChange.Unlock()
	if err := os.Remove(credentialPath()); err != nil && !os.IsNotExist(err) {
		return errors.New("не удалось удалить сохранённую сессию")
	}
	c.authMu.Lock()
	c.auth = nil
	c.authResult = AuthStatus{}
	c.authCheckedAt = time.Time{}
	c.authEpoch.Add(1)
	c.authMu.Unlock()
	c.clearPrivateCache()
	return nil
}

func (c *Catalog) clearPrivateCache() {
	c.mu.Lock()
	c.cache = map[string]catalogEntry{}
	c.streams = map[string]streamInfo{}
	c.mu.Unlock()
}

func (s *Server) authCommand(ctx context.Context, r request) response {
	switch r.Action {
	case "auth-accounts", "auth-discover":
		auth := r.Auth
		if r.Action == "auth-accounts" {
			auth = s.catalog.credentials()
		}
		if auth == nil {
			return s.catalogError(errNoAccount)
		}
		accounts, err := s.catalog.discoverAccounts(ctx, auth)
		if err != nil {
			return s.catalogError(err)
		}
		if r.Action == "auth-accounts" {
			for i := range accounts {
				accounts[i].Selected = accounts[i].AuthUser == auth.AuthUser && accounts[i].PageID == auth.PageID
			}
		}
		return response{Accounts: accounts}
	case "auth-select":
		if err := s.catalog.switchAccount(ctx, r.Query); err != nil {
			return s.catalogError(err)
		}
		status := s.catalog.authStatus()
		return response{Auth: &status}
	case "auth-import":
		if err := s.catalog.replaceAuth(ctx, r.Auth); err != nil {
			return response{Error: err.Error()}
		}
		status := s.catalog.authStatus()
		return response{Auth: &status}
	case "auth-logout":
		if err := s.catalog.logout(); err != nil {
			return response{Error: err.Error()}
		}
		// Do not keep stale personal search results selectable after logout.
		s.mu.Lock()
		s.latestSearch = nil
		s.mu.Unlock()
		return response{Auth: &AuthStatus{}}
	case "auth-check":
		auth := s.catalog.credentials()
		err := s.catalog.checkAuth(ctx, auth)
		status := s.catalog.authStatus()
		if err != nil {
			return response{Auth: &status, Error: err.Error()}
		}
		return response{Auth: &status}
	default:
		status := s.catalog.authStatus()
		return response{Auth: &status}
	}

}

func printAuth(status *AuthStatus) {
	switch {
	case !status.Configured:
		fmt.Println("YouTube Music: вход не выполнен. ytm-alice login")
	case status.Checked && status.Valid:
		fmt.Println("YouTube Music: вход подтверждён, аккаунт подключён.")
	case status.Checked:
		fmt.Println("YouTube Music: сессия сохранена, проверка входа не прошла.")
	default:
		fmt.Println("YouTube Music: сессия сохранена. Проверка: ytm-alice auth check")
	}
}

func isLikedQuery(q string) bool {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "мои лайки", "понравившиеся", "liked":
		return true
	}
	return false
}
