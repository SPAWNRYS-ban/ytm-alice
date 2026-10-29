package main

import (
	"context"
	"log"
	"net"
	"slices"
	"strings"
	"time"
)

// The firmware-specific speech adapter is shared by standalone and Luna modes.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r == ',' || r == '.' || r == '!' || r == '?' || r == '«' || r == '»' {
			return -1
		}
		return r
	}, strings.ToLower(s))), " ")
}
func voiceAction(text string) (string, string, bool) {
	q := normalize(text)
	q = strings.TrimSpace(strings.TrimPrefix(q, "алиса "))
	q = strings.TrimSpace(strings.TrimSuffix(q, " пожалуйста"))
	if q == "поставь мои лайки" || q == "включи мои лайки" || q == "включи понравившиеся" {
		return "play", "мои лайки", true
	}
	controls := map[string]string{"дальше": "next", "следующий трек": "next", "следующая песня": "next", "назад": "previous", "предыдущий трек": "previous", "предыдущая песня": "previous", "пауза": "pause", "поставь на паузу": "pause", "продолжи": "resume", "продолжи музыку": "resume", "стоп": "stop", "выключи музыку": "stop", "выключи ютуб": "stop", "что сейчас играет": "what", "что играет": "what"}
	if a, ok := controls[q]; ok {
		return a, "", true
	}
	for _, p := range []string{"включи на ютубе ", "включи в ютубе ", "включи на youtube ", "включи youtube music ", "включи ютуб музыку ", "включи ютуб ", "поставь на ютубе ", "поставь в ютубе ", "поставь на youtube ", "поставь youtube music ", "поставь ютуб ", "ютуб музыка ", "youtube music "} {
		if strings.HasPrefix(q, p) {
			query := strings.TrimSpace(q[len(p):])
			if query != "" {
				return "play", query, true
			}
		}
	}
	for _, verb := range []string{"поставь ", "включи "} {
		if !strings.HasPrefix(q, verb) {
			continue
		}
		query := strings.TrimSpace(strings.TrimPrefix(q, verb))
		// Explicit music nouns take precedence: a song may itself contain words
		// such as "свет" or "таймер". Retain album/playlist nouns for the catalog.
		for _, noun := range []string{"песню ", "трек ", "композицию ", "музыку ", "альбом ", "плейлист "} {
			if strings.HasPrefix(query, noun) {
				if noun != "альбом " && noun != "плейлист " {
					query = strings.TrimSpace(strings.TrimPrefix(query, noun))
				}
				if query != "" {
					return "play", query, true
				}
			}
		}
		// Bare "включи" can name an arbitrary smart-home device or scene.
		// "Поставь <artist/title>" is the shorthand for music; native device,
		// timer and built-in content requests still belong to Alice.
		if verb == "поставь " && query != "" && !nativeVoiceTarget(query) {
			return "play", query, true
		}
	}
	return "", "", false
}

func nativeVoiceTarget(query string) bool {
	words := strings.Fields(query)
	if len(words) == 0 {
		return true
	}
	first := words[0]
	for _, stem := range []string{"на", "в", "песню", "трек", "композицию", "альбом", "плейлист", "музык", "мою", "мой", "мои", "свою", "любим", "что-нибудь", "таймер", "будильник", "напомин", "свет", "ламп", "люстр", "розет", "телевизор", "кондиционер", "увлажн", "пылесос", "чайник", "температур", "отоплен", "режим", "громк", "звук", "радио", "сказк", "подкаст", "новост", "погод", "блютуз", "bluetooth"} {
		if (stem == "на" || stem == "в") && first != stem {
			continue
		}
		if strings.HasPrefix(first, stem) {
			return true
		}
	}
	for _, word := range words {
		for _, unit := range []string{"минут", "секунд", "градус", "процент"} {
			if strings.HasPrefix(word, unit) {
				return true
			}
		}
	}
	return false
}
func (s *Server) voiceLoop(ctx context.Context) {
	last := ""
	lastTime := time.Time{}
	var trackRequests []string
	for ctx.Err() == nil {
		c, e := net.DialTimeout("tcp", ipcAddress(), time.Second)
		if e != nil {
			time.Sleep(time.Second)
			continue
		}
		for ctx.Err() == nil {
			c.SetReadDeadline(time.Now().Add(2 * time.Second))
			raw, e := frameRead(c)
			if e != nil {
				break
			}
			ev := decodeAlice(raw)
			if ev.HasState {
				s.mu.Lock()
				p := s.player
				s.mu.Unlock()
				if p != nil {
					if ev.State == 0 {
						p.send("volume 1")
					} else {
						p.send("volume 0.2")
					}
				}
			}
			q := normalize(ev.Text)
			a, _, ok := voiceAction(q)
			if ev.HasState && ev.State != 3 && !(a == "what" && ev.State == 1 && ev.Final) {
				continue
			}
			if !ok {
				// Let a native music request take over without mixing two players.
				if strings.HasPrefix(strings.TrimPrefix(q, "алиса "), "включи музыку") || strings.Contains(q, "включи мою волну") {
					s.mu.Lock()
					s.stopLocked()
					s.state.Status = "stopped"
					s.state.Error = ""
					s.state.PositionMS = 0
					s.saveLocked()
					s.mu.Unlock()
				}
				continue
			}
			if a != "play" {
				if s.luna != nil && !s.luna.selected.Load() {
					continue
				}
				v := s.snapshot()
				if v.Status != "playing" && v.Status != "paused" && v.Status != "buffering" && v.Status != "error" {
					continue
				}
				// Native Bluetooth control already calls MPRIS; a second Next
				// from our speech listener would skip two tracks.
				if s.mediaConnected.Load() && (a == "next" || a == "previous" || a == "pause" || a == "resume") {
					continue
				}
			}
			if a == "what" && ev.RequestID != "" {
				if slices.Contains(trackRequests, ev.RequestID) {
					continue
				}
				trackRequests = append(trackRequests, ev.RequestID)
				if len(trackRequests) > 64 {
					trackRequests = trackRequests[1:]
				}
				// A final ASR result can precede BUSY. Cancel on the existing
				// event connection before the cloud starts music recognition.
				c.SetWriteDeadline(time.Now().Add(time.Second))
				if err := frameWrite(c, rpc(8, pi(1, 3))); err != nil {
					log.Print("early track-query cancellation not confirmed")
				}
			} else if q == last && time.Since(lastTime) < 10*time.Second {
				continue
			}
			last = q
			lastTime = time.Now()
			log.Printf("voice route: %s", a)
			go func(text string) {
				c, cancel := context.WithTimeout(ctx, 45*time.Second)
				defer cancel()
				res := s.command(c, request{Action: "voice", Query: text})
				if res.Error != "" {
					log.Print("voice music request failed")
				}
			}(ev.Text)
		}
		c.Close()
	}
}
