package main

// YouTube Music "Cast to TV" receiver: DIAL/SSDP discovery plus the YouTube
// Lounge (MDX) protocol. The phone's YouTube Music app finds the Station as a
// TV, sends a pairing code over DIAL, and then controls playback through
// www.youtube.com/api/lounge/bc/bind. Protocol reference: yt-cast-receiver
// (patrickkfkan, MIT) and peer-dial.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	castPort       = 8008
	castPrefix     = "/ytcr"
	loungeBase     = "https://www.youtube.com/api/lounge"
	castTheme      = "m" // YouTube Music senders
	castScreenApp  = "ytcr"
	ssdpAddr       = "239.255.255.250:1900"
	dialServiceURN = "urn:dial-multiscreen-org:service:dial:1"
)

type castConfig struct {
	DialUUID string `json:"dial_uuid"`
	DeviceID string `json:"device_id"`
	ScreenID string `json:"screen_id"`
	Name     string `json:"name"`
}

type castMessage struct {
	AID     int
	Name    string
	Payload any
}

type castOut struct {
	name    string
	payload map[string]string
}

type castReceiver struct {
	s    *Server
	http *http.Client
	cfg  castConfig

	mu         sync.Mutex
	loungeTok  string
	sid        string
	gsid       string
	aid        int
	rid        int
	ofs        int
	listID     string // playlist received from the sender, "" if the queue is ours
	senders    int
	lastSig    string
	lastReport time.Time
	lastPosMS  int64 // position at lastPosAt, to notice seeks
	lastPosAt  time.Time
	sendMu     sync.Mutex
	session    context.CancelFunc
}

func castEnabled() bool { return os.Getenv("YTM_ALICE_CAST") != "0" }

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func castConfigPath() string { return filepath.Join(dataDir(), "cast.json") }

func loadCastConfig() castConfig {
	var c castConfig
	if raw, e := os.ReadFile(castConfigPath()); e == nil {
		json.Unmarshal(raw, &c)
	}
	if c.DialUUID == "" {
		c.DialUUID = newUUID()
	}
	if c.DeviceID == "" {
		c.DeviceID = newUUID()
	}
	if n := os.Getenv("YTM_ALICE_CAST_NAME"); n != "" {
		c.Name = n
	}
	if c.Name == "" {
		c.Name = "Яндекс Станция"
		if h, e := os.Hostname(); e == nil && strings.Contains(strings.ToLower(h), "mini2") {
			c.Name = "Яндекс Станция Мини 2"
		}
	}
	return c
}

func (c *castReceiver) saveConfig() {
	raw, _ := json.Marshal(c.cfg)
	os.WriteFile(castConfigPath()+".tmp", raw, 0600)
	os.Rename(castConfigPath()+".tmp", castConfigPath())
}

func (s *Server) castLoop(ctx context.Context) {
	if !castEnabled() {
		return
	}
	c := &castReceiver{s: s, http: &http.Client{Timeout: 0}, cfg: loadCastConfig()}
	c.saveConfig()
	go c.serveDIAL(ctx)
	go c.serveSSDP(ctx)
	go c.reportLoop(ctx)
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := c.runSession(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 2*time.Minute {
			backoff = 5 * time.Second
		}
		log.Printf("cast: lounge session ended: %v; retry in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Minute {
			backoff *= 2
		}
	}
}

// ---------- SSDP / DIAL ----------

func outboundIP(remote net.IP) net.IP {
	conn, err := net.Dial("udp4", net.JoinHostPort(remote.String(), "1900"))
	if err != nil {
		return nil
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP
}

func (c *castReceiver) location(ip net.IP) string {
	return fmt.Sprintf("http://%s:%d%s/ssdp/device-desc.xml", ip, castPort, castPrefix)
}

func (c *castReceiver) serveSSDP(ctx context.Context) {
	group, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
	for ctx.Err() == nil {
		conn, err := net.ListenMulticastUDP("udp4", nil, group)
		if err != nil {
			log.Print("cast: ssdp: ", err)
			time.Sleep(10 * time.Second)
			continue
		}
		go func() { <-ctx.Done(); conn.Close() }()
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				break
			}
			req := string(buf[:n])
			if !strings.HasPrefix(req, "M-SEARCH") {
				continue
			}
			st := ""
			for _, line := range strings.Split(req, "\r\n") {
				if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "ST") {
					st = strings.TrimSpace(v)
				}
			}
			if st != dialServiceURN && st != "ssdp:all" && st != "urn:dial-multiscreen-org:device:dial:1" {
				continue
			}
			if st == "ssdp:all" {
				st = dialServiceURN
			}
			ip := outboundIP(from.IP)
			if ip == nil {
				continue
			}
			resp := "HTTP/1.1 200 OK\r\n" +
				"CACHE-CONTROL: max-age=1800\r\n" +
				"DATE: " + time.Now().UTC().Format(http.TimeFormat) + "\r\n" +
				"EXT:\r\n" +
				"LOCATION: " + c.location(ip) + "\r\n" +
				"SERVER: Linux/4.9 UPnP/1.1 ytm-alice/" + version + "\r\n" +
				"ST: " + st + "\r\n" +
				"USN: uuid:" + c.cfg.DialUUID + "::" + st + "\r\n" +
				"BOOTID.UPNP.ORG: 7337\r\nCONFIGID.UPNP.ORG: 7337\r\n\r\n"
			if out, err := net.DialUDP("udp4", nil, from); err == nil {
				out.Write([]byte(resp))
				out.Close()
			}
		}
		conn.Close()
	}
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func (c *castReceiver) serveDIAL(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc(castPrefix+"/ssdp/device-desc.xml", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host + castPrefix
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("Application-URL", base+"/apps")
		w.Header().Set("Access-Control-Expose-Headers", "Location")
		fmt.Fprintf(w, `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <URLBase>%s</URLBase>
  <device>
    <deviceType>urn:dial-multiscreen-org:device:dial:1</deviceType>
    <friendlyName>%s</friendlyName>
    <manufacturer>Yandex</manufacturer>
    <modelName>ytm-alice</modelName>
    <UDN>uuid:%s</UDN>
    <serviceList><service>
      <serviceType>urn:dial-multiscreen-org:service:dial:1</serviceType>
      <serviceId>urn:dial-multiscreen-org:serviceId:dial</serviceId>
      <controlURL>/ssdp/notfound</controlURL><eventSubURL>/ssdp/notfound</eventSubURL><SCPDURL>/ssdp/notfound</SCPDURL>
    </service></serviceList>
  </device>
</root>`, xmlEscape(base), xmlEscape(c.cfg.Name), c.cfg.DialUUID)
	})
	mux.HandleFunc(castPrefix+"/apps", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc(castPrefix+"/apps/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, castPrefix+"/apps/")
		app, sub, _ := strings.Cut(rest, "/")
		if app != "YouTube" {
			http.NotFound(w, r)
			return
		}
		base := "http://" + r.Host + castPrefix
		switch {
		case r.Method == http.MethodGet && sub == "":
			c.mu.Lock()
			state := "stopped"
			if c.senders > 0 {
				state = "running"
			}
			c.mu.Unlock()
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<service xmlns="urn:dial-multiscreen-org:schemas:dial" dialVer="1.7">
  <name>YouTube</name>
  <options allowStop="false"/>
  <state>%s</state>
  <link rel="run" href="run"/>
</service>`, state)
		case r.Method == http.MethodPost && sub == "":
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
			q, _ := url.ParseQuery(string(body))
			code := q.Get("pairingCode")
			theme := q.Get("theme")
			log.Printf("cast: DIAL launch theme=%q pairing=%v", theme, code != "")
			if code == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if err := c.registerPairingCode(r.Context(), code); err != nil {
				log.Print("cast: register pairing code: ", err)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Location", base+"/apps/YouTube/run")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && sub == "dial_data":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusMethodNotAllowed)
		default:
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Addr: fmt.Sprintf(":%d", castPort), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	if err := srv.ListenAndServe(); err != nil && ctx.Err() == nil {
		log.Print("cast: DIAL server: ", err)
	}
}

// ---------- Lounge protocol ----------

func (c *castReceiver) post(ctx context.Context, u string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(cctx))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return b, resp.StatusCode, err
}

func (c *castReceiver) generateScreenID(ctx context.Context) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, loungeBase+"/pairing/generate_screen_id", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	id := strings.TrimSpace(string(b))
	if resp.StatusCode != 200 || id == "" || len(id) > 200 {
		return "", fmt.Errorf("generate_screen_id: HTTP %d", resp.StatusCode)
	}
	return id, nil
}

func (c *castReceiver) loungeToken(ctx context.Context) (string, int64, error) {
	b, code, err := c.post(ctx, loungeBase+"/pairing/get_lounge_token_batch", url.Values{"screen_ids": {c.cfg.ScreenID}})
	if err != nil {
		return "", 0, err
	}
	var v struct {
		Screens []struct {
			LoungeToken     string `json:"loungeToken"`
			RefreshInterval int64  `json:"refreshIntervalInMillis"`
		} `json:"screens"`
	}
	if code != 200 || json.Unmarshal(b, &v) != nil || len(v.Screens) == 0 || v.Screens[0].LoungeToken == "" {
		return "", 0, fmt.Errorf("get_lounge_token_batch: HTTP %d", code)
	}
	return v.Screens[0].LoungeToken, v.Screens[0].RefreshInterval, nil
}

func (c *castReceiver) registerPairingCode(ctx context.Context, code string) error {
	c.mu.Lock()
	screen := c.cfg.ScreenID
	c.mu.Unlock()
	if screen == "" {
		return errors.New("lounge session not ready")
	}
	_, status, err := c.post(ctx, loungeBase+"/pairing/register_pairing_code", url.Values{
		"access_type":  {"permanent"},
		"app":          {castScreenApp},
		"pairing_code": {code},
		"screen_id":    {screen},
		"screen_name":  {c.cfg.Name},
		"device_id":    {c.cfg.DeviceID},
	})
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("register_pairing_code: HTTP %d", status)
	}
	return nil
}

// bindQuery builds the query string for /bc/bind. kind: init | send | rpc. Caller holds c.mu.
func (c *castReceiver) bindQuery(kind string) string {
	v := url.Values{}
	v.Set("device", "LOUNGE_SCREEN")
	v.Set("id", c.cfg.DeviceID)
	v.Set("obfuscatedGaiaId", "")
	v.Set("name", c.cfg.Name)
	v.Set("app", castScreenApp)
	v.Set("theme", castTheme)
	v.Set("capabilities", "dsp,mic,dpa,ntb")
	v.Set("cst", "m")
	v.Set("mdxVersion", "2")
	v.Set("loungeIdToken", c.loungeTok)
	v.Set("VER", "8")
	v.Set("v", "2")
	v.Set("zx", randHex(6))
	v.Set("t", "1")
	devInfo := `{"brand":"Yandex","model":"Station","year":0,"os":"Linux","osVersion":"4.9","chipset":"","clientName":"TVHTML5","dialAdditionalDataSupportLevel":"unsupported","mdxDialServerType":"MDX_DIAL_SERVER_TYPE_UNKNOWN"}`
	switch kind {
	case "init":
		v.Set("deviceInfo", devInfo)
		v.Set("RID", strconv.Itoa(c.rid))
		v.Set("CVER", "1")
		c.rid++
	case "send":
		v.Set("deviceInfo", devInfo)
		v.Set("SID", c.sid)
		v.Set("RID", strconv.Itoa(c.rid))
		v.Set("AID", strconv.Itoa(c.aid))
		v.Set("gsessionid", c.gsid)
		c.rid++
	case "rpc":
		v.Set("RID", "rpc")
		v.Set("SID", c.sid)
		v.Set("CI", "0")
		v.Set("AID", strconv.Itoa(c.aid))
		v.Set("gsessionid", c.gsid)
		v.Set("TYPE", "xmlhttp")
	}
	return v.Encode()
}

var castMsgRe = regexp.MustCompile(`\[(\d+),\["(.+?)"(?:,(.*?))?\]\]`)

func parseCastMessages(data string) []castMessage {
	data = strings.NewReplacer("\r", "", "\n", "").Replace(data)
	var out []castMessage
	for _, m := range castMsgRe.FindAllStringSubmatch(data, -1) {
		aid, _ := strconv.Atoi(m[1])
		msg := castMessage{AID: aid, Name: m[2]}
		if m[3] != "" {
			var arr []any
			if json.Unmarshal([]byte("["+m[3]+"]"), &arr) == nil {
				if len(arr) == 1 {
					msg.Payload = arr[0]
				} else {
					msg.Payload = arr
				}
			}
		}
		out = append(out, msg)
	}
	return out
}

func (c *castReceiver) noteMessages(msgs []castMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range msgs {
		if m.AID > c.aid {
			c.aid = m.AID
		}
		switch m.Name {
		case "c":
			if a, ok := m.Payload.([]any); ok && len(a) > 0 {
				c.sid, _ = a[0].(string)
			}
		case "S":
			c.gsid, _ = m.Payload.(string)
		}
	}
}

func (c *castReceiver) runSession(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	c.session = cancel
	c.sid, c.gsid, c.loungeTok = "", "", ""
	c.aid, c.ofs = -1, 0
	c.rid = 41000 + int(time.Now().UnixNano()%9000)
	screen := c.cfg.ScreenID
	c.mu.Unlock()

	if screen == "" {
		id, err := c.generateScreenID(ctx)
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.cfg.ScreenID = id
		c.mu.Unlock()
		c.saveConfig()
	}
	tok, refresh, err := c.loungeToken(ctx)
	if err != nil {
		// A stale screen id: start over with a fresh one next time.
		c.mu.Lock()
		c.cfg.ScreenID = ""
		c.mu.Unlock()
		c.saveConfig()
		return err
	}
	c.mu.Lock()
	c.loungeTok = tok
	q := c.bindQuery("init")
	c.mu.Unlock()
	body, status, err := c.post(ctx, loungeBase+"/bc/bind?"+q, url.Values{"count": {"0"}})
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("bind init: HTTP %d", status)
	}
	msgs := parseCastMessages(string(body))
	c.noteMessages(msgs)
	c.mu.Lock()
	ready := c.sid != "" && c.gsid != ""
	c.mu.Unlock()
	if !ready {
		return errors.New("bind init: no SID/gsessionid")
	}
	log.Printf("cast: lounge session ready as %q (DIAL :%d)", c.cfg.Name, castPort)
	for _, m := range msgs {
		if m.Name != "c" && m.Name != "S" {
			c.handle(ctx, m)
		}
	}
	if refresh <= 0 || refresh > int64(24*time.Hour/time.Millisecond) {
		refresh = int64(12 * time.Hour / time.Millisecond)
	}
	deadline := time.Now().Add(time.Duration(refresh) * time.Millisecond)
	for ctx.Err() == nil {
		if time.Now().After(deadline) {
			return errors.New("lounge token refresh")
		}
		if err := c.poll(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// poll holds one long-poll RPC connection open and dispatches its messages.
func (c *castReceiver) poll(ctx context.Context) error {
	c.mu.Lock()
	q := c.bindQuery("rpc")
	c.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, loungeBase+"/bc/bind?"+q, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("bind rpc: HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		msgs := parseCastMessages(sc.Text())
		if len(msgs) == 0 {
			continue
		}
		c.noteMessages(msgs)
		for _, m := range msgs {
			c.handle(ctx, m)
		}
	}
	return sc.Err()
}

func (c *castReceiver) send(ctx context.Context, msgs ...castOut) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	if c.sid == "" {
		c.mu.Unlock()
		return
	}
	q := c.bindQuery("send")
	form := url.Values{"count": {strconv.Itoa(len(msgs))}, "ofs": {strconv.Itoa(c.ofs)}}
	c.ofs += len(msgs)
	c.mu.Unlock()
	for i, m := range msgs {
		p := fmt.Sprintf("req%d_", i)
		form.Set(p+"_sc", m.name)
		for k, v := range m.payload {
			form.Set(p+k, v)
		}
	}
	_, status, err := c.post(ctx, loungeBase+"/bc/bind?"+q, form)
	if err != nil || status != 200 {
		log.Printf("cast: send %s: status=%d err=%v", msgs[0].name, status, err)
		if status == 400 || status == 404 || status == 410 {
			c.mu.Lock()
			if c.session != nil {
				c.session()
			}
			c.mu.Unlock()
		}
	}
}

// ---------- state mapping ----------

func castStatus(st State) int {
	switch st.Status {
	case "playing":
		return 1
	case "paused":
		return 2
	case "buffering", "searching":
		return 3
	case "stopped", "error":
		if len(st.Queue) > 0 {
			return 4
		}
	}
	return -1
}

func (c *castReceiver) nowPlaying(st State) castOut {
	p := map[string]string{}
	if st.Index >= 0 && st.Index < len(st.Queue) && castStatus(st) != -1 {
		dur := st.Queue[st.Index].DurationMS / 1000
		pos := strconv.FormatFloat(float64(st.PositionMS)/1000, 'f', 3, 64)
		p["videoId"] = st.Queue[st.Index].ID
		p["currentTime"] = pos
		p["duration"] = strconv.FormatInt(dur, 10)
		p["state"] = strconv.Itoa(castStatus(st))
		p["cpn"] = "ytmalice"
		p["loadedTime"] = strconv.FormatInt(dur, 10)
		p["seekableStartTime"] = "0"
		p["seekableEndTime"] = strconv.FormatInt(dur, 10)
		c.mu.Lock()
		if c.listID != "" {
			p["listId"] = c.listID
			p["currentIndex"] = strconv.Itoa(st.Index)
		}
		c.mu.Unlock()
	}
	return castOut{"nowPlaying", p}
}

func (c *castReceiver) stateChange(st State) castOut {
	dur := int64(0)
	if st.Index >= 0 && st.Index < len(st.Queue) {
		dur = st.Queue[st.Index].DurationMS / 1000
	}
	return castOut{"onStateChange", map[string]string{
		"state":             strconv.Itoa(castStatus(st)),
		"currentTime":       strconv.FormatFloat(float64(st.PositionMS)/1000, 'f', 3, 64),
		"duration":          strconv.FormatInt(dur, 10),
		"loadedTime":        strconv.FormatInt(dur, 10),
		"seekableStartTime": "0",
		"seekableEndTime":   strconv.FormatInt(dur, 10),
		"cpn":               "ytmalice",
	}}
}

func navInfo(st State) castOut {
	return castOut{"onHasPreviousNextChanged", map[string]string{
		"hasPrevious": strconv.FormatBool(st.Index > 0 && len(st.Queue) > 0),
		"hasNext":     strconv.FormatBool(st.Index+1 < len(st.Queue)),
	}}
}

func alsaVolume() (int, bool) {
	out, err := exec.Command("amixer", "-c0", "sget", "Master").Output()
	if err != nil {
		return 0, false
	}
	m := regexp.MustCompile(`\[(\d+)%\]`).FindStringSubmatch(string(out))
	if m == nil {
		return 0, false
	}
	v, _ := strconv.Atoi(m[1])
	return v, strings.Contains(string(out), "[off]")
}

func setAlsaVolume(level int) {
	if level < 0 {
		level = 0
	}
	if level > 100 {
		level = 100
	}
	exec.Command("amixer", "-q", "-c0", "sset", "Master", strconv.Itoa(level)+"%").Run()
}

func volumeMsg() castOut {
	v, muted := alsaVolume()
	return castOut{"onVolumeChanged", map[string]string{"volume": strconv.Itoa(v), "muted": strconv.FormatBool(muted)}}
}

// reportLoop pushes player changes to connected senders.
func (c *castReceiver) reportLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		active := c.senders > 0 && c.sid != ""
		c.mu.Unlock()
		if !active {
			continue
		}
		st := c.s.snapshot()
		cur := ""
		if st.Index >= 0 && st.Index < len(st.Queue) {
			cur = st.Queue[st.Index].ID
		}
		sig := fmt.Sprintf("%s|%s|%d|%d", st.Status, cur, st.Index, len(st.Queue))
		c.mu.Lock()
		changed := sig != c.lastSig
		// A seek keeps status and track but moves the position; senders need it.
		expected := c.lastPosMS
		if st.Status == "playing" {
			expected += time.Since(c.lastPosAt).Milliseconds()
		}
		if d := st.PositionMS - expected; d > 3000 || d < -3000 {
			changed = true
		}
		c.lastPosMS, c.lastPosAt = st.PositionMS, time.Now()
		periodic := st.Status == "playing" && time.Since(c.lastReport) > 15*time.Second
		if changed || periodic {
			c.lastSig = sig
			c.lastReport = time.Now()
		}
		c.mu.Unlock()
		if changed {
			c.send(ctx, c.nowPlaying(st), c.stateChange(st), navInfo(st))
		} else if periodic {
			c.send(ctx, c.stateChange(st))
		}
	}
}

// ---------- incoming commands ----------

func pstr(p any, key string) string {
	m, ok := p.(map[string]any)
	if !ok {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func (c *castReceiver) handle(ctx context.Context, m castMessage) {
	if m.Name != "noop" {
		log.Printf("cast: <- %s", m.Name)
	}
	st := c.s.snapshot()
	switch m.Name {
	case "loungeStatus":
		var devs []map[string]any
		json.Unmarshal([]byte(pstr(m.Payload, "devices")), &devs)
		n := 0
		for _, d := range devs {
			if d["type"] == "REMOTE_CONTROL" {
				n++
			}
		}
		c.mu.Lock()
		c.senders = n
		c.lastSig = ""
		c.mu.Unlock()
		go c.send(ctx, navInfo(st), c.nowPlaying(st), c.stateChange(st),
			castOut{"onAutoplayModeChanged", map[string]string{"autoplayMode": "UNSUPPORTED"}}, volumeMsg())
	case "remoteConnected":
		c.mu.Lock()
		c.senders++
		c.lastSig = ""
		c.mu.Unlock()
		go c.send(ctx, navInfo(st), c.nowPlaying(st), c.stateChange(st), volumeMsg())
	case "remoteDisconnected":
		c.mu.Lock()
		if c.senders > 0 {
			c.senders--
		}
		c.mu.Unlock()
	case "getNowPlaying":
		go c.send(ctx, c.nowPlaying(st))
	case "getVolume":
		go c.send(ctx, volumeMsg())
	case "setVolume":
		if v, err := strconv.Atoi(pstr(m.Payload, "volume")); err == nil {
			setAlsaVolume(v)
		}
		go c.send(ctx, volumeMsg())
	case "setAutoplayMode":
		go c.send(ctx, castOut{"onAutoplayModeChanged", map[string]string{"autoplayMode": "UNSUPPORTED"}})
	case "setPlaylist", "updatePlaylist":
		go c.loadPlaylist(ctx, m)
	case "play":
		go c.s.command(ctx, request{Action: "resume"})
	case "pause":
		go c.s.command(ctx, request{Action: "pause"})
	case "stopVideo":
		c.mu.Lock()
		c.listID = ""
		c.mu.Unlock()
		go c.s.command(ctx, request{Action: "stop"})
	case "next", "previous":
		go c.s.command(ctx, request{Action: m.Name})
	case "seekTo":
		if f, err := strconv.ParseFloat(pstr(m.Payload, "newTime"), 64); err == nil && !math.IsNaN(f) && f >= 0 && f <= 7200 {
			ms := int64(f * 1000)
			// Acknowledge at once: the sender waits for the new position.
			loading := c.stateChange(st)
			loading.payload["state"] = "3"
			loading.payload["currentTime"] = strconv.FormatFloat(f, 'f', 3, 64)
			c.mu.Lock()
			c.lastSig = ""
			c.lastPosMS, c.lastPosAt = ms, time.Now()
			c.mu.Unlock()
			go c.send(ctx, loading)
			go c.s.castSeek(ms)
		}
	}
}

func (c *castReceiver) loadPlaylist(ctx context.Context, m castMessage) {
	listID := pstr(m.Payload, "listId")
	var ids []string
	for _, id := range strings.Split(pstr(m.Payload, "videoIds"), ",") {
		if videoID.MatchString(id) {
			ids = append(ids, id)
		}
	}
	if listID == "" || len(ids) == 0 {
		if m.Name == "updatePlaylist" {
			c.mu.Lock()
			c.listID = ""
			c.mu.Unlock()
		}
		return
	}
	tracks := c.s.castTracks(ctx, ids)
	c.mu.Lock()
	c.listID = listID
	c.lastSig = ""
	c.mu.Unlock()
	if m.Name == "updatePlaylist" {
		c.s.castUpdateQueue(tracks)
		return
	}
	idx, _ := strconv.Atoi(pstr(m.Payload, "currentIndex"))
	if want := pstr(m.Payload, "videoId"); want != "" && (idx < 0 || idx >= len(tracks) || tracks[idx].ID != want) {
		for i, t := range tracks {
			if t.ID == want {
				idx = i
				break
			}
		}
	}
	if idx < 0 || idx >= len(tracks) {
		idx = 0
	}
	// Senders repeat setPlaylist for the same track; keep playing instead of
	// resolving the stream again.
	if st := c.s.snapshot(); st.Index >= 0 && st.Index < len(st.Queue) && st.Queue[st.Index].ID == tracks[idx].ID &&
		(st.Status == "playing" || st.Status == "paused" || st.Status == "buffering") {
		c.s.castUpdateQueue(tracks)
		return
	}
	offset := int64(0)
	if f, err := strconv.ParseFloat(pstr(m.Payload, "currentTime"), 64); err == nil && f > 1 {
		offset = int64(f * 1000)
	}
	c.s.castPlay(tracks, idx, offset)
}

// castTracks resolves titles for the sender's queue; unknown ids keep a placeholder.
func (s *Server) castTracks(ctx context.Context, ids []string) []Track {
	byID := map[string]Track{}
	for start := 0; start < len(ids); start += 50 {
		end := min(start+50, len(ids))
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		d, err := s.catalog.request(cctx, "music/get_queue", map[string]any{"videoIds": ids[start:end]})
		cancel()
		if err != nil {
			log.Print("cast: get_queue: ", err)
			continue
		}
		for _, t := range parseTracks(d) {
			byID[t.ID] = t
		}
	}
	tracks := make([]Track, 0, len(ids))
	for _, id := range ids {
		if t, ok := byID[id]; ok {
			tracks = append(tracks, t)
		} else {
			tracks = append(tracks, Track{ID: id, Title: id, ThumbnailURL: fallbackThumbnail(id)})
		}
	}
	return tracks
}

func (s *Server) castPlay(tracks []Track, idx int, offset int64) {
	s.mu.Lock()
	s.stopLocked()
	s.queueCast = true
	s.state.Queue = tracks
	s.state.Index = idx
	s.state.PositionMS = 0
	s.state.Error = ""
	s.saveLocked()
	s.mu.Unlock()
	s.start(offset, false)
}

func (s *Server) castUpdateQueue(tracks []Track) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := ""
	if s.state.Index >= 0 && s.state.Index < len(s.state.Queue) {
		cur = s.state.Queue[s.state.Index].ID
	}
	for i, t := range tracks {
		if t.ID == cur {
			s.state.Queue = tracks
			s.state.Index = i
			s.saveLocked()
			return
		}
	}
}

func (s *Server) castSeek(ms int64) {
	s.command(context.Background(), request{Action: "seek", PositionMS: ms})
}
