package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const lunaPluginID = "local.ytm-alice"

func lunaPackageInstalled() bool {
	_, err := os.Stat(filepath.Join(packageRoot(), "share/luna/plugins", lunaPluginID, "plugin.toml"))
	return err == nil
}

func managementLuna() (*lunaClient, error) {
	base := os.Getenv("YTM_ALICE_LUNA_URL")
	if base == "" {
		base = "http://127.0.0.1:8099"
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("YTM_ALICE_LUNA_URL должен указывать на локальную Luna")
	}
	return &lunaClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 22 * time.Second}, id: lunaPluginID}, nil
}

func lunaDaemon(action string) error {
	l, err := managementLuna()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	endpoint := "/api/plugins/" + lunaPluginID
	switch action {
	case "install":
		// Save settings through Luna so startup and explicit configuration agree.
		var settings map[string]any
		if err = l.request(ctx, "/api/settings", nil, &settings); err != nil {
			return err
		}
		settings["allow_plugin_binaries"] = true
		settings["updates"] = map[string]any{"enabled": false, "interval_minutes": 360}
		if err = l.requestMethod(ctx, http.MethodPut, "/api/settings", settings, nil); err != nil {
			return err
		}
		if err = l.request(ctx, "/api/plugins/rescan", struct{}{}, nil); err != nil {
			return err
		}
		var plugins []struct {
			ID       string `json:"id"`
			Manifest struct {
				ID string `json:"id"`
			} `json:"manifest"`
			Compatible bool   `json:"compatible"`
			Reason     string `json:"incompatibility"`
		}
		if err = l.request(ctx, "/api/plugins", nil, &plugins); err != nil {
			return err
		}
		found := false
		for _, plugin := range plugins {
			if plugin.Manifest.ID == lunaPluginID || plugin.ID == lunaPluginID {
				found = true
				if !plugin.Compatible {
					return fmt.Errorf("нужна Luna с поддержкой бинарных пакетов YAM: %s", plugin.Reason)
				}
			}
		}
		if !found {
			return errors.New("Luna не видит пакет YAM; проверьте YAM_ROOT")
		}
		if err = bundledMedia("install"); err != nil {
			return err
		}
		if err = bundledMedia("start"); err != nil {
			return err
		}
		// The old boot hook must not start a second copy alongside Luna.
		if raw, e := os.ReadFile(bootPath); e == nil {
			if !strings.Contains(string(raw), bootMarker) {
				return errors.New("автозапуск принадлежит другому установщику")
			}
			if err = exec.Command("mount", "-o", "remount,rw", "/").Run(); err != nil {
				return err
			}
			err = os.Remove(bootPath)
			restore := exec.Command("mount", "-o", "remount,ro", "/").Run()
			if err != nil {
				return err
			}
			if restore != nil {
				return restore
			}
		}
		if err = l.request(ctx, endpoint+"/service/enable", struct{}{}, nil); err != nil {
			return err
		}
		fmt.Println("ytm-alice установлен; запуском управляет Luna.")
	case "start":
		return l.request(ctx, endpoint+"/service/enable", struct{}{}, nil)
	case "stop":
		return l.request(ctx, endpoint+"/stop", struct{}{}, nil)
	case "restart":
		if err = l.request(ctx, endpoint+"/stop", struct{}{}, nil); err != nil {
			return err
		}
		return l.request(ctx, endpoint+"/service/enable", struct{}{}, nil)
	case "uninstall":
		if err = l.request(ctx, endpoint+"/service/disable", struct{}{}, nil); err != nil {
			return err
		}
		return bundledMedia("uninstall")
	case "status":
		var status any
		if err = l.request(ctx, "/api/media", nil, &status); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	default:
		return errors.New("неизвестная команда daemon")
	}
	return nil
}
