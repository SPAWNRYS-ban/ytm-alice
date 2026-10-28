package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

type protoField struct {
	wire int
	n    uint64
	b    []byte
}

func pv(v uint64) []byte        { return binary.AppendUvarint(nil, v) }
func pi(k int, v uint64) []byte { return append(pv(uint64(k)<<3), pv(v)...) }
func pb(k int, v []byte) []byte {
	b := append(pv(uint64(k)<<3|2), pv(uint64(len(v)))...)
	return append(b, v...)
}
func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}
func parseProto(b []byte) (map[int]protoField, error) {
	out := map[int]protoField{}
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 || key>>3 == 0 {
			return nil, errors.New("invalid protobuf key")
		}
		b = b[n:]
		f := protoField{wire: int(key & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errors.New("invalid protobuf number")
			}
			f.n = v
			b = b[n:]
		case 1, 5:
			size := 8
			if f.wire == 5 {
				size = 4
			}
			if len(b) < size {
				return nil, io.ErrUnexpectedEOF
			}
			f.b = b[:size]
			b = b[size:]
		case 2:
			v, n := binary.Uvarint(b)
			if n <= 0 || v > uint64(len(b)-n) {
				return nil, io.ErrUnexpectedEOF
			}
			b = b[n:]
			f.b = b[:int(v)]
			b = b[int(v):]
		default:
			return nil, errors.New("unsupported protobuf wire")
		}
		out[int(key>>3)] = f
	}
	return out, nil
}
func proto(b []byte) map[int]protoField { d, _ := parseProto(b); return d }
func rpc(field int, payload []byte) []byte {
	return cat(pb(252, cat(pb(7, []byte("AliceCapability")), pb(field, payload))), pi(253, 210))
}
func frameWrite(c net.Conn, b []byte) error {
	if len(b) > 2<<20 {
		return errors.New("IPC frame too large")
	}
	h := make([]byte, 4)
	binary.LittleEndian.PutUint32(h, uint32(len(b)))
	_, err := io.Copy(c, bytesReader(cat(h, b)))
	return err
}

// bytesReader is kept here so all framing writes use a complete-write operation.
type byteReader struct{ b []byte }

func bytesReader(b []byte) *byteReader { return &byteReader{b} }
func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}
func frameRead(c net.Conn) ([]byte, error) {
	h := make([]byte, 4)
	if _, err := io.ReadFull(c, h); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(h)
	if n == 0 || n > 2<<20 {
		return nil, errors.New("invalid IPC length")
	}
	b := make([]byte, int(n))
	_, err := io.ReadFull(c, b)
	return b, err
}
func ipcAddress() string {
	raw, err := os.ReadFile("/system/vendor/quasar/quasar.cfg")
	if err != nil {
		return "127.0.0.1:9875"
	}
	var conf map[string]json.RawMessage
	var alice struct {
		Port int `json:"port"`
	}
	if json.Unmarshal(raw, &conf) == nil && json.Unmarshal(conf["aliced"], &alice) == nil && alice.Port > 0 {
		return "127.0.0.1:" + strconv.Itoa(alice.Port)
	}
	return "127.0.0.1:9875"
}
func cancelDialog() error {
	c, err := net.DialTimeout("tcp", ipcAddress(), 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	return frameWrite(c, rpc(8, pi(1, 3)))
}
func speak(text string) error {
	return nativeSpeech(text, false)
}

func speakOwned(text string) error {
	return nativeSpeech(text, true)
}

func nativeSpeech(text string, takeDialog bool) error {
	if len([]rune(text)) > 1000 {
		return errors.New("speech text too long")
	}
	c, err := net.DialTimeout("tcp", ipcAddress(), 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	return speakOnConnection(c, text, takeDialog)
}

func speakOnConnection(c net.Conn, text string, takeDialog bool) error {
	c.SetDeadline(time.Now().Add(8 * time.Second))
	if err := frameWrite(c, rpc(20, pb(1, []byte("AliceCapability")))); err != nil {
		return err
	}
	if takeDialog {
		// Keep cancellation and vocalization on one live connection. A
		// separate fire-and-forget cancel can be dropped or race the reply.
		if err := frameWrite(c, rpc(8, pi(1, 3))); err != nil {
			return err
		}
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			frame, err := frameRead(c)
			if err != nil {
				return fmt.Errorf("native dialog cancellation not confirmed: %w", err)
			}
			event := decodeAlice(frame)
			if event.HasState && (event.State == 0 || event.State == 6) {
				break
			}
		}
		c.SetDeadline(time.Now().Add(8 * time.Second))
	}
	if err := frameWrite(c, rpc(8, cat(pi(1, 18), pi(3, 1), pb(13, cat(pb(1, []byte(text)), pi(2, 1)))))); err != nil {
		return err
	}
	started := false
	for {
		b, err := frameRead(c)
		if err != nil {
			return fmt.Errorf("no native TTS completion: %w", err)
		}
		env := proto(b)
		if env[253].n != 210 {
			continue
		}
		body := proto(env[252].b)
		event := proto(body[18].b)
		switch event[1].n {
		case 6:
			started = true
		case 1:
			if started {
				return nil
			}
		case 4:
			return errors.New("native TTS error")
		}
	}
}

type aliceEvent struct {
	Text      string
	State     uint64
	HasState  bool
	Final     bool
	RequestID string
}

func decodeAlice(b []byte) aliceEvent {
	d := proto(b)
	if d[253].wire == 0 && d[253].n == 3 {
		d = map[int]protoField{3: {wire: 2, b: d[252].b}}
	}
	state := proto(d[3].b)
	_, hasState := d[3]
	e := aliceEvent{Text: string(state[2].b), State: state[1].n, HasState: hasState, RequestID: string(state[7].b)}
	recognition := proto(state[15].b)
	e.Final = recognition[2].n == 1 && recognition[4].n == 1 && string(recognition[1].b) == e.Text
	source := proto(state[10].b)
	if string(source[6].b) == "command" {
		req := proto(state[15].b)
		if req[2].n != 0 {
			e.Text = string(req[1].b)
		}
	}
	return e
}
