package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Sign-in through the YouTube TV app: a one-time code entered on another device
// yields a refresh token that does not rotate like browser cookies. YouTube's
// internal API accepts only tokens of its own TV client, so the client id and
// secret are read from the public youtube.com/tv script, as SmartTube does.
// The token is used only with the TVHTML5 client on www.youtube.com.

const (
	tvOrigin    = "https://www.youtube.com"
	tvUserAgent = "Mozilla/5.0 (ChromiumStylePlatform) Cobalt/Version"
	tvScope     = "http://gdata.youtube.com https://www.googleapis.com/auth/youtube-paid-content"
)

// TVToken is stored only in youtube-session.json, like browser cookies.
type TVToken struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

func (TVToken) String() string   { return "<YouTube TV token>" }
func (TVToken) GoString() string { return "<YouTube TV token>" }

type tvClient struct{ id, secret string }

type tvDeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type expiredError string

func (e expiredError) Error() string        { return string(e) }
func (e expiredError) Is(target error) bool { return target == errSessionExpired }

var errTVExpired error = expiredError("Вход через YouTube TV отозван или истёк. Войди заново по коду в настройках.")
var errTVPending = errors.New("код ещё не подтверждён")
var errTVNetwork = errors.New("YouTube TV: network unavailable")

var (
	tvBaseScript  = regexp.MustCompile(`<script[^>]*\bid="base-js"[^>]*\bsrc="(/s/[^"]+)"|<script[^>]*\bsrc="(/s/[^"]+)"[^>]*\bid="base-js"`)
	tvClientField = regexp.MustCompile(`clientId:"([-\w]+\.apps\.googleusercontent\.com)",\s*[$\w]+:"([-\w]+)"`)
	tvSecretValue = regexp.MustCompile(`^[-\w]{1,200}$`)
)

func (t *TVToken) validate() error {
	if t == nil || len(t.ClientID) > 200 || !strings.HasSuffix(t.ClientID, ".apps.googleusercontent.com") || !tvSecretValue.MatchString(strings.TrimSuffix(t.ClientID, ".apps.googleusercontent.com")) {
		return errors.New("некорректный клиент YouTube TV")
	}
	if !tvSecretValue.MatchString(t.ClientSecret) {
		return errors.New("некорректный секрет клиента YouTube TV")
	}
	if len(t.RefreshToken) == 0 || len(t.RefreshToken) > 2048 {
		return errors.New("некорректный токен YouTube TV")
	}
	for _, ch := range t.RefreshToken {
		if ch <= 32 || ch >= 127 {
			return errors.New("некорректный токен YouTube TV")
		}
	}
	return nil
}

func (c *Catalog) tvGet(ctx context.Context, path string, limit int64) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", tvOrigin+path, nil)
	req.Header.Set("User-Agent", tvUserAgent)
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("YouTube TV: network unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.New("YouTube TV: страница входа недоступна")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", errors.New("YouTube TV: network unavailable")
	}
	return string(raw), nil
}

// The client changes rarely; keep it for a day to avoid a 1 MB download per refresh.
func (c *Catalog) tvClientCredentials(ctx context.Context) (tvClient, error) {
	c.tvMu.Lock()
	cached, at := c.tvClient, c.tvClientAt
	c.tvMu.Unlock()
	if cached.id != "" && time.Since(at) < 24*time.Hour {
		return cached, nil
	}
	page, err := c.tvGet(ctx, "/tv", 2<<20)
	if err != nil {
		return tvClient{}, err
	}
	m := tvBaseScript.FindStringSubmatch(page)
	if m == nil {
		return tvClient{}, errors.New("YouTube TV: не найден скрипт входа; вход по коду временно недоступен")
	}
	path := m[1] + m[2]
	if strings.Contains(path, "//") || strings.Contains(path, "..") {
		return tvClient{}, errors.New("YouTube TV: неожиданный адрес скрипта входа")
	}
	script, err := c.tvGet(ctx, path, 8<<20)
	if err != nil {
		return tvClient{}, err
	}
	f := tvClientField.FindStringSubmatch(script)
	if f == nil {
		return tvClient{}, errors.New("YouTube TV: не найден клиент входа; вход по коду временно недоступен")
	}
	client := tvClient{id: f[1], secret: f[2]}
	c.tvMu.Lock()
	c.tvClient, c.tvClientAt = client, time.Now()
	c.tvMu.Unlock()
	return client, nil
}

type tvOAuthReply struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

func (c *Catalog) tvOAuth(ctx context.Context, path string, form url.Values, out any) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", tvOrigin+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", tvUserAgent)
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return 0, errTVNetwork
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out); err != nil {
		return resp.StatusCode, errors.New("YouTube TV: некорректный ответ сервера входа")
	}
	return resp.StatusCode, nil
}

func (c *Catalog) tvStartDevice(ctx context.Context, client tvClient, deviceID string) (tvDeviceCode, error) {
	var code tvDeviceCode
	status, err := c.tvOAuth(ctx, "/o/oauth2/device/code", url.Values{"client_id": {client.id}, "scope": {tvScope}, "device_id": {deviceID}, "device_model": {"ytlr::"}}, &code)
	if err != nil {
		return code, err
	}
	if status != 200 || code.DeviceCode == "" || code.UserCode == "" || len(code.UserCode) > 32 {
		return code, errors.New("YouTube TV: не удалось получить код входа")
	}
	if u, e := url.Parse(code.VerificationURL); e != nil || u.Scheme != "https" || !(u.Hostname() == "www.google.com" || u.Hostname() == "google.com") {
		code.VerificationURL = "https://www.google.com/device"
	}
	if code.Interval < 5 {
		code.Interval = 5
	}
	if code.ExpiresIn <= 0 || code.ExpiresIn > 3600 {
		code.ExpiresIn = 1800
	}
	return code, nil
}

// tvPollDevice returns errTVPending until the code is confirmed; slowDown asks to poll less often.
func (c *Catalog) tvPollDevice(ctx context.Context, client tvClient, deviceCode string) (token *TVToken, slowDown bool, err error) {
	var reply tvOAuthReply
	if _, err := c.tvOAuth(ctx, "/o/oauth2/token", url.Values{"client_id": {client.id}, "client_secret": {client.secret}, "code": {deviceCode}, "grant_type": {"http://oauth.net/grant_type/device/1.0"}}, &reply); err != nil {
		return nil, false, err
	}
	switch reply.Error {
	case "":
	case "authorization_pending":
		return nil, false, errTVPending
	case "slow_down":
		return nil, true, errTVPending
	case "access_denied":
		return nil, false, errors.New("вход отклонён на странице Google")
	case "expired_token":
		return nil, false, errors.New("код входа истёк; запроси новый")
	default:
		return nil, false, errors.New("YouTube TV: вход не выполнен")
	}
	token = &TVToken{ClientID: client.id, ClientSecret: client.secret, RefreshToken: reply.RefreshToken}
	if token.validate() != nil {
		return nil, false, errors.New("YouTube TV: сервер не выдал токен обновления")
	}
	c.rememberTVAccess(token, reply.AccessToken, reply.ExpiresIn)
	return token, false, nil
}

func (c *Catalog) rememberTVAccess(token *TVToken, access string, expiresIn int) {
	if access == "" || expiresIn <= 0 {
		return
	}
	c.tvMu.Lock()
	c.tvAccess, c.tvAccessFor = access, token.RefreshToken
	// Refresh a minute early so a long request does not cross the expiry.
	c.tvAccessUntil = time.Now().Add(time.Duration(expiresIn)*time.Second - time.Minute)
	c.tvMu.Unlock()
}

// tvAccessToken returns a short-lived access token, refreshing it when needed.
func (c *Catalog) tvAccessToken(ctx context.Context, token *TVToken) (string, error) {
	if err := token.validate(); err != nil {
		return "", err
	}
	c.tvMu.Lock()
	if c.tvAccessFor == token.RefreshToken && time.Now().Before(c.tvAccessUntil) {
		access := c.tvAccess
		c.tvMu.Unlock()
		return access, nil
	}
	c.tvMu.Unlock()
	var reply tvOAuthReply
	status, err := c.tvOAuth(ctx, "/o/oauth2/token", url.Values{"client_id": {token.ClientID}, "client_secret": {token.ClientSecret}, "refresh_token": {token.RefreshToken}, "grant_type": {"refresh_token"}}, &reply)
	if err != nil {
		return "", err
	}
	if reply.Error == "invalid_grant" || reply.Error == "unauthorized_client" || reply.Error == "invalid_client" {
		return "", errTVExpired
	}
	if status != 200 || reply.AccessToken == "" {
		return "", errors.New("YouTube TV: не удалось обновить вход")
	}
	c.rememberTVAccess(token, reply.AccessToken, reply.ExpiresIn)
	return reply.AccessToken, nil
}

func (c *Catalog) forgetTVAccess() {
	c.tvMu.Lock()
	c.tvAccess, c.tvAccessFor, c.tvAccessUntil = "", "", time.Time{}
	c.tvMu.Unlock()
}
