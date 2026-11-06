package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultWebAddr = ":24721"

type Settings struct {
	WebHosts       []string          `json:"web_hosts"`
	WebAddr        string            `json:"web_addr"`
	Radio          bool              `json:"radio"`
	Shuffle        bool              `json:"shuffle"`
	AnnounceText   string            `json:"announce_text"`
	AnnounceErrors bool              `json:"announce_errors"`
	PlaybackLED    bool              `json:"playback_led"`
	VoiceCommands  map[string]string `json:"voice_commands"`
}

type SettingsPatch struct {
	WebHosts       []string          `json:"web_hosts,omitempty"`
	WebAddr        *string           `json:"web_addr,omitempty"`
	Radio          *bool             `json:"radio,omitempty"`
	Shuffle        *bool             `json:"shuffle,omitempty"`
	AnnounceText   *string           `json:"announce_text,omitempty"`
	AnnounceErrors *bool             `json:"announce_errors,omitempty"`
	PlaybackLED    *bool             `json:"playback_led,omitempty"`
	VoiceCommands  map[string]string `json:"voice_commands"`
}

func defaultSettings() Settings {
	return Settings{WebAddr: defaultWebAddr, Radio: true, AnnounceText: "Включаю", AnnounceErrors: true, PlaybackLED: true, VoiceCommands: map[string]string{}}
}

func settingsPath() string { return filepath.Join(dataDir(), "settings.json") }

func (s *Server) settingsLocked() Settings {
	value := defaultSettings()
	if s.settings != nil {
		value = *s.settings
	}
	value.VoiceCommands = cloneCommands(value.VoiceCommands)
	value.WebHosts = append([]string(nil), value.WebHosts...)
	return value
}

func cloneCommands(commands map[string]string) map[string]string {
	copy := map[string]string{}
	for phrase, action := range commands {
		copy[phrase] = action
	}
	return copy
}

func (s *Server) getSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settingsLocked()
}

func validateSettings(value Settings) error {
	if len(value.WebHosts) > 16 {
		return errors.New("можно разрешить до 16 доменов панели")
	}
	for _, host := range value.WebHosts {
		if !validWebHost(host) {
			return errors.New("web_hosts: нужны точные имена доменов без схемы, порта и пути")
		}
	}

	if value.WebAddr != "off" {
		host, port, err := net.SplitHostPort(value.WebAddr)
		n, numErr := strconv.Atoi(port)
		if err != nil || numErr != nil || n < 1 || n > 65535 || (host != "" && host != "localhost" && net.ParseIP(host) == nil) {
			return errors.New("адрес панели: :24721, IP:порт или off")
		}
		if n == 8008 || n == 18883 {
			return errors.New("порт занят службой трансляции или аудиоплеером")
		}
	}
	if len([]rune(value.AnnounceText)) > 200 || strings.ContainsAny(value.AnnounceText, "\r\n\x00") {
		return errors.New("фраза перед музыкой: до 200 символов, одна строка")
	}
	if len(value.VoiceCommands) > 40 {
		return errors.New("можно добавить до 40 голосовых команд")
	}
	for phrase, action := range value.VoiceCommands {
		if len([]rune(phrase)) > 120 || normalize(phrase) != phrase || phrase == "" {
			return errors.New("голосовая команда: до 120 символов, строчными буквами")
		}
		switch action {
		case "play":
			if !strings.HasSuffix(phrase, " {query}") || strings.Count(phrase, "{query}") != 1 {
				return errors.New("команда поиска должна заканчиваться на {query}")
			}
		case "pause", "resume", "stop", "next", "previous", "what", "recommendations", "shuffle-on", "shuffle-off":
			if strings.ContainsAny(phrase, "{}") {
				return errors.New("{query} используется только для поиска")
			}
		default:
			return errors.New("неизвестное действие голосовой команды")
		}
	}
	return nil
}

func writeSettings(value Settings) error {
	if err := os.MkdirAll(dataDir(), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dataDir(), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(value)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), settingsPath())
	}
	return err
}

func (s *Server) loadSettings() error {
	value := defaultSettings()
	f, err := os.Open(settingsPath())
	if os.IsNotExist(err) {
		// Preserve bookmarked URLs when upgrading an existing installation.
		if _, err := os.Stat(filepath.Join(dataDir(), "state.json")); err == nil {
			value.WebAddr = ":80"
		}
		if err := writeSettings(value); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		defer f.Close()
		decoder := json.NewDecoder(io.LimitReader(f, 32<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
			return errors.New("повреждён settings.json")
		}
	}
	if err := validateSettings(value); err != nil {
		return err
	}
	s.settings = &value
	return nil
}

func (s *Server) settingsCommand(r request) response {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.settingsLocked()
	if r.Action == "config-set" {
		p := r.Settings
		if p == nil {
			return response{Error: "нужны настройки"}
		}
		if p.WebHosts != nil {
			value.WebHosts = append([]string{}, p.WebHosts...)
		}
		if p.WebAddr != nil {
			value.WebAddr = *p.WebAddr
		}
		if p.Radio != nil {
			value.Radio = *p.Radio
		}
		if p.Shuffle != nil {
			value.Shuffle = *p.Shuffle
		}
		if p.AnnounceText != nil {
			value.AnnounceText = *p.AnnounceText
		}
		if p.AnnounceErrors != nil {
			value.AnnounceErrors = *p.AnnounceErrors
		}
		if p.PlaybackLED != nil {
			value.PlaybackLED = *p.PlaybackLED
		}
		if p.VoiceCommands != nil {
			value.VoiceCommands = cloneCommands(p.VoiceCommands)
		}
		if err := validateSettings(value); err != nil {
			return response{Error: err.Error()}
		}
		if p.PlaybackLED != nil && !*p.PlaybackLED && len(playbackLEDPaths("/sys/class/leds")) == 0 {
			return response{Error: "управление подсветкой этой модели пока не поддерживается"}
		}
		if err := writeSettings(value); err != nil {
			return response{Error: "не удалось сохранить настройки"}
		}
		s.settings = &value
	}
	address := s.webAddr
	if address == "" {
		address = effectiveWebAddr(value)
	}
	return response{Settings: &value, WebAddr: address, LEDControl: len(playbackLEDPaths("/sys/class/leds")) > 0}
}

func effectiveWebAddr(settings Settings) string {
	if value := os.Getenv("YTM_ALICE_WEB_ADDR"); value != "" {
		return value
	}
	return settings.WebAddr
}

func configRequest(args []string) (request, error) {
	if len(args) == 0 || (len(args) == 1 && args[0] == "get") {
		return request{Action: "config-get"}, nil
	}
	if len(args) < 3 || args[0] != "set" {
		return request{}, errors.New("config get | config set КЛЮЧ ЗНАЧЕНИЕ")
	}
	key, value := args[1], strings.Join(args[2:], " ")
	var raw any = value
	switch key {
	case "radio", "shuffle", "playback_led", "announce_errors":
		v, err := strconv.ParseBool(value)
		if err != nil {
			return request{}, errors.New("нужно true или false")
		}
		raw = v
	case "web_hosts":
		var hosts []string
		if json.Unmarshal([]byte(value), &hosts) != nil || hosts == nil {
			return request{}, errors.New("нужен JSON-массив доменов")
		}
		raw = hosts
	case "voice_commands":
		var commands map[string]string
		if json.Unmarshal([]byte(value), &commands) != nil || commands == nil {
			return request{}, errors.New("нужен JSON-объект голосовых команд")
		}
		raw = commands
	case "web_addr", "announce_text":
	default:
		return request{}, fmt.Errorf("неизвестная настройка %s", key)
	}
	data, _ := json.Marshal(map[string]any{key: raw})
	var patch SettingsPatch
	if err := json.Unmarshal(data, &patch); err != nil {
		return request{}, err
	}
	return request{Action: "config-set", Settings: &patch}, nil
}
