package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"
)

// TVLoginState is shown to the user while a code waits for confirmation.
// The device code and tokens never leave the daemon.
type TVLoginState struct {
	Status          string `json:"status"` // idle, waiting, done, failed
	UserCode        string `json:"user_code,omitempty"`
	VerificationURL string `json:"verification_url,omitempty"`
	ExpiresAt       int64  `json:"expires_at,omitempty"`
	Error           string `json:"error,omitempty"`
}

// One poll interval step; tests shorten it.
var tvPollUnit = time.Second

func (c *Catalog) tvLoginStatus() TVLoginState {
	c.tvLoginMu.Lock()
	defer c.tvLoginMu.Unlock()
	if c.tvLogin.Status == "" {
		return TVLoginState{Status: "idle"}
	}
	return c.tvLogin
}

func (c *Catalog) setTVLogin(id uint64, state TVLoginState) {
	c.tvLoginMu.Lock()
	defer c.tvLoginMu.Unlock()
	if c.tvLoginID == id {
		c.tvLogin = state
	}
}

func (c *Catalog) cancelTVLogin() {
	c.tvLoginMu.Lock()
	defer c.tvLoginMu.Unlock()
	if c.tvLoginCancel != nil {
		c.tvLoginCancel()
		c.tvLoginCancel = nil
	}
	c.tvLoginID++
	c.tvLogin = TVLoginState{}
}

// startTVLogin requests a code and waits for it in the background, so the
// control socket is not held for the minutes a person needs to enter it.
func (c *Catalog) startTVLogin(ctx context.Context) (TVLoginState, error) {
	c.cancelTVLogin()
	client, err := c.tvClientCredentials(ctx)
	if err != nil {
		return TVLoginState{}, err
	}
	seed := make([]byte, 16)
	rand.Read(seed)
	code, err := c.tvStartDevice(ctx, client, hex.EncodeToString(seed))
	if err != nil {
		return TVLoginState{}, err
	}
	expires := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	state := TVLoginState{Status: "waiting", UserCode: code.UserCode, VerificationURL: code.VerificationURL, ExpiresAt: expires.Unix()}
	wait, cancel := context.WithDeadline(context.Background(), expires)
	c.tvLoginMu.Lock()
	c.tvLoginID++
	id := c.tvLoginID
	c.tvLogin, c.tvLoginCancel = state, cancel
	c.tvLoginMu.Unlock()
	go c.waitTVLogin(wait, cancel, id, client, code, state, tvPollUnit)
	return state, nil
}

func (c *Catalog) waitTVLogin(ctx context.Context, cancel context.CancelFunc, id uint64, client tvClient, code tvDeviceCode, state TVLoginState, unit time.Duration) {
	defer cancel()
	interval := code.Interval
	fail := func(err error) {
		state.Status, state.Error, state.UserCode = "failed", err.Error(), ""
		c.setTVLogin(id, state)
	}
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				fail(errors.New("код входа истёк; запроси новый"))
			}
			return
		case <-time.After(time.Duration(interval) * unit):
		}
		token, slowDown, err := c.tvPollDevice(ctx, client, code.DeviceCode)
		if errors.Is(err, errTVPending) || errors.Is(err, errTVNetwork) {
			if slowDown && interval < 60 {
				interval += 5
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				continue // reported by the Done branch
			}
			fail(err)
			return
		}
		// Keep working cookies of the previous sign-in for 18+ tracks.
		if current := c.credentials(); current.cookieSession() != nil {
			hybrid := *current.cookieSession()
			hybrid.TV = token
			if c.replaceAuth(ctx, &hybrid) == nil {
				c.setTVLogin(id, TVLoginState{Status: "done"})
				return
			}
		}
		if err := c.replaceAuth(ctx, &Credentials{TV: token}); err != nil {
			fail(err)
			return
		}
		c.setTVLogin(id, TVLoginState{Status: "done"})
		return
	}
}

func printTVLogin(state *TVLoginState) {
	switch state.Status {
	case "waiting":
		fmt.Printf("Открой %s и введи код: %s\n", state.VerificationURL, state.UserCode)
	case "done":
		fmt.Println("YouTube TV: вход выполнен.")
	case "failed":
		fmt.Println("YouTube TV: " + state.Error)
	default:
		fmt.Println("YouTube TV: вход по коду не запущен. ytm-alice login tv")
	}
}

// tvLoginCLI prints the code and waits for the daemon to finish the sign-in.
func tvLoginCLI(asJSON bool) int {
	out, err := call(request{Action: "auth-tv-start"})
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if asJSON {
		printResponse(out, true)
		return 0
	}
	printTVLogin(out.TVLogin)
	fmt.Println("Жду подтверждения… (Ctrl+C — прервать ожидание здесь; вход продолжится на колонке)")
	for {
		time.Sleep(3 * time.Second)
		out, err = call(request{Action: "auth-tv-status"})
		if err == nil && out.TVLogin == nil {
			err = errors.New(out.Error)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		switch out.TVLogin.Status {
		case "waiting":
			continue
		case "done":
			printTVLogin(out.TVLogin)
			if out.Auth != nil {
				printAuth(out.Auth)
			}
			return 0
		default:
			printTVLogin(out.TVLogin)
			return 1
		}
	}
}
