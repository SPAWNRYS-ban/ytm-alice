package main

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"time"
)

var errSessionExpired = errors.New("Вход в YouTube истёк. Открой настройки и загрузи свежий cookies.txt.")
var errNoAccount = errors.New("Подключи YouTube Music в настройках, чтобы открыть личную библиотеку.")
var errNoTracks = errors.New("треки не найдены")

type YouTubeAccount struct {
	AuthUser     string `json:"auth_user"`
	PageID       string `json:"page_id,omitempty"`
	Name         string `json:"name"`
	Handle       string `json:"handle,omitempty"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	Selected     bool   `json:"selected"`
}

func parseAccounts(root any, fallback string) []YouTubeAccount {
	result := []YouTubeAccount{}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			if renderer, ok := item["accountItemRenderer"]; ok {
				row, _ := renderer.(map[string]any)
				if row["isDisabled"] == true || row["hasChannel"] != true {
					return
				}
				name := text(get(row, "accountName"))
				endpoint := get(row, "serviceEndpoint", "selectActiveIdentityEndpoint")
				tokens, _ := get(endpoint, "supportedTokens").([]any)
				if name == "" || len(tokens) == 0 {
					return
				}
				account := YouTubeAccount{AuthUser: fallback, Name: name, Handle: text(get(row, "channelHandle")), ThumbnailURL: thumbnailURL(map[string]any{"thumbnail": get(row, "accountPhoto")}), Selected: row["isSelected"] == true}
				for _, token := range tokens {
					if page := str(get(token, "pageIdToken", "pageId")); page != "" {
						account.PageID = page
					}
					if index := get(token, "accountSessionToken", "sessionIndex"); index != nil {
						switch index := index.(type) {
						case string:
							account.AuthUser = index
						case float64:
							account.AuthUser = strconv.Itoa(int(index))
						}
					}
					if raw := str(get(token, "accountSigninToken", "signinUrl")); raw != "" {
						if parsed, err := url.Parse(raw); err == nil {
							if index := parsed.Query().Get("authuser"); index != "" {
								account.AuthUser = index
							}
						}
					}
				}
				if !validAccountIndex(account.AuthUser) || !validPageID(account.PageID) {
					return
				}
				key := account.AuthUser + ":" + account.PageID
				if !seen[key] {
					seen[key] = true
					result = append(result, account)
				}
				return
			}
			keys := make([]string, 0, len(item))
			for key := range item {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(item[key])
			}
		case []any:
			for _, entry := range item {
				walk(entry)
			}
		}
	}
	walk(root)
	if len(result) > 30 {
		result = result[:30]
	}
	return result
}
func validAccountIndex(value string) bool {
	n, err := strconv.Atoi(value)
	return err == nil && n >= 0 && n <= 99
}
func validPageID(value string) bool {
	if len(value) > 40 {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
func (c *Catalog) discoverAccounts(ctx context.Context, auth *Credentials) ([]YouTubeAccount, error) {
	if err := auth.validate(); err != nil {
		return nil, err
	}
	base := *auth
	base.PageID = ""
	data, err := c.requestWithClient(ctx, "account/accounts_list", map[string]any{"requestType": "ACCOUNTS_LIST_REQUEST_TYPE_CHANNEL_SWITCHER", "callCircumstance": "SWITCHING_USERS_FULL"}, &base, "WEB")
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			c.setAuthResult(auth, nil, err)
		}
		return nil, err
	}
	accounts := parseAccounts(data, base.AuthUser)
	if len(accounts) == 0 {
		c.setAuthResult(auth, nil, errSessionExpired)
		return nil, errSessionExpired
	}
	return accounts, nil
}
func (c *Catalog) accountInfo(ctx context.Context, auth *Credentials) (*YouTubeAccount, error) {
	if auth == nil {
		return nil, errNoAccount
	}
	if auth.PageID != "" {
		accounts, err := c.discoverAccounts(ctx, auth)
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			if account.PageID == auth.PageID && account.AuthUser == auth.AuthUser {
				return &account, nil
			}
		}
		return nil, errors.New("Этот профиль YouTube больше недоступен. Выбери аккаунт заново в настройках.")
	}
	data, err := c.requestWithAuth(ctx, "account/account_menu", map[string]any{}, auth)
	if err != nil {
		return nil, err
	}
	header := get(data, "actions", 0, "openPopupAction", "popup", "multiPageMenuRenderer", "header", "activeAccountHeaderRenderer")
	name := text(get(header, "accountName"))
	if name == "" {
		return nil, errSessionExpired
	}
	return &YouTubeAccount{AuthUser: auth.AuthUser, Name: name, Handle: text(get(header, "channelHandle")), ThumbnailURL: thumbnailURL(map[string]any{"thumbnail": get(header, "accountPhoto")}), Selected: true}, nil
}
func (c *Catalog) authStatus() AuthStatus {
	c.authMu.RLock()
	defer c.authMu.RUnlock()
	if c.auth == nil {
		return AuthStatus{}
	}
	status := c.authResult
	status.Configured = true
	return status
}
func (c *Catalog) setAuthResult(auth *Credentials, account *YouTubeAccount, err error) {
	if auth == nil {
		return
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.auth == nil || *c.auth != *auth {
		return
	}
	c.authCheckedAt = time.Now()
	c.authResult = AuthStatus{Configured: true, Checked: err == nil || errors.Is(err, errSessionExpired), Valid: err == nil, Expired: errors.Is(err, errSessionExpired), Account: account}
}
func (c *Catalog) requireAuth(ctx context.Context) error {
	auth := c.credentials()
	if auth == nil {
		return errNoAccount
	}
	c.authMu.RLock()
	status, checked := c.authResult, c.authCheckedAt
	c.authMu.RUnlock()
	if status.Expired {
		return errSessionExpired
	}
	if status.Checked && status.Valid && time.Since(checked) < time.Minute {
		return nil
	}
	return c.checkAuth(ctx, auth)
}
func (s *Server) catalogError(err error) response {
	out := response{Error: err.Error()}
	if errors.Is(err, errNoAccount) || errors.Is(err, errSessionExpired) {
		status := s.catalog.authStatus()
		out.Auth = &status
	}
	return out
}

func responseLoggedOut(root any) bool {
	return get(root, "responseContext", "mainAppWebResponseContext", "loggedOut") == true || get(root, "responseContext", "webResponseContextExtensionData", "ytConfigData", "loggedOut") == true
}

// Only descriptive account fields reach the UI. Sign-in URLs, tokens and cookies
// stay out of responses and state. Delegated PageID selects an existing channel.

func (c *Catalog) switchAccount(ctx context.Context, selected string) error {
	c.authChange.Lock()
	defer c.authChange.Unlock()
	auth := c.credentials()
	if auth == nil {
		return errNoAccount
	}
	accounts, err := c.discoverAccounts(ctx, auth)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if account.AuthUser+":"+account.PageID == selected {
			auth.AuthUser, auth.PageID = account.AuthUser, account.PageID
			return c.replaceAuthLocked(ctx, auth)
		}
	}
	return errors.New("Профиль не найден. Обнови список аккаунтов.")
}
