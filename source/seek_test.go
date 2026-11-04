package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func buildSidx(timescale uint32, first uint32, refs [][2]uint32) []byte {
	b := make([]byte, 32+12*len(refs))
	binary.BigEndian.PutUint32(b[0:], uint32(len(b)))
	copy(b[4:], "sidx")
	// version 0, flags 0, reference_ID 1
	binary.BigEndian.PutUint32(b[12:], 1)
	binary.BigEndian.PutUint32(b[16:], timescale)
	binary.BigEndian.PutUint32(b[20:], 0) // earliest presentation time
	binary.BigEndian.PutUint32(b[24:], first)
	binary.BigEndian.PutUint16(b[30:], uint16(len(refs)))
	for i, r := range refs {
		p := 32 + 12*i
		binary.BigEndian.PutUint32(b[p:], r[0])
		binary.BigEndian.PutUint32(b[p+4:], r[1])
	}
	return b
}

func TestParseSidx(t *testing.T) {
	// 44.1 kHz timescale, three 10 s fragments.
	box := buildSidx(44100, 0, [][2]uint32{{1000, 441000}, {2000, 441000}, {3000, 441000}})
	refs, err := parseSidx(box, 632)
	if err != nil {
		t.Fatal(err)
	}
	end := int64(632 + len(box))
	want := []sidxRef{{end, 1000, 0, 10000, 0}, {end + 1000, 2000, 10000, 10000, 441000}, {end + 3000, 3000, 20000, 10000, 882000}}
	if len(refs) != len(want) {
		t.Fatalf("refs %+v", refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("ref %d = %+v, want %+v", i, refs[i], want[i])
		}
	}
	if _, err := parseSidx(box[:20], 0); err == nil {
		t.Fatal("truncated box must fail")
	}
}

func TestSeekWindowLength(t *testing.T) {
	w := &seekWindow{head: make([]byte, 632), fragStart: 5000}
	if got := w.length(streamInfo{length: 15000}); got != 632+10000 {
		t.Fatalf("length %d", got)
	}
}

func box(kind string, body []byte) []byte {
	b := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:], kind)
	copy(b[8:], body)
	return b
}

func fragment(tfdt uint32, payload string) []byte {
	t := make([]byte, 8)
	binary.BigEndian.PutUint32(t[4:], tfdt) // version 0, flags 0
	traf := box("traf", append(box("tfhd", make([]byte, 8)), box("tfdt", t)...))
	moof := box("moof", append(box("mfhd", make([]byte, 8)), traf...))
	return append(moof, box("mdat", []byte(payload))...)
}

func TestTfdtWriterShiftsFragments(t *testing.T) {
	f1 := fragment(441000, "audio-1")
	f2 := fragment(882000, "audio-2")
	stream := append(append([]byte{}, f1...), f2...)
	base := int64(5000)
	rec := httptest.NewRecorder()
	w := &tfdtWriter{ResponseWriter: rec, pos: base, frags: []int64{base, base + int64(len(f1))}, shift: 441000}
	for i := 0; i < len(stream); i += 7 { // small chunks split boxes
		end := min(i+7, len(stream))
		if n, err := w.Write(stream[i:end]); err != nil || n != end-i {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	w.flush()
	out := rec.Body.Bytes()
	if len(out) != len(stream) {
		t.Fatalf("length %d != %d", len(out), len(stream))
	}
	want := append(fragment(0, "audio-1"), fragment(441000, "audio-2")...)
	if !bytes.Equal(out, want) {
		t.Fatalf("tfdt not shifted")
	}
}

func TestSeekRelayRangesMatchRebasedVirtualFile(t *testing.T) {
	f1, f2 := fragment(441000, "audio-1"), fragment(882000, "audio-2")
	base := int64(5000)
	data := append(make([]byte, base), append(f1, f2...)...)
	want := append([]byte("init"), append(fragment(0, "audio-1"), fragment(441000, "audio-2")...)...)
	s, p := streamFixture(data)
	p.window = &seekWindow{head: []byte("init"), fragStart: base, startMS: 10000, startTicks: 441000, frags: []int64{base, base + int64(len(f1))}}
	s.catalog.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		start, end := streamSpan(t, r)
		return streamResponse(data[start:end+1], start, end, len(data)), nil
	})
	// Include ranges that start/end within tfdt, not only fragment boundaries.
	for _, span := range []string{"", "bytes=0-2", "bytes=50-63", "bytes=63-65", "bytes=65-", "bytes=-17", fmt.Sprintf("bytes=%d-%d", 4+len(f1)+50, 4+len(f1)+63)} {
		t.Run(span, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://127.0.0.1/audio/test", nil)
			r.Header.Set("Range", span)
			out := httptest.NewRecorder()
			s.relay(out, r)
			start, end, err := byteRange(span, int64(len(want)))
			if err != nil || !bytes.Equal(out.Body.Bytes(), want[start:end+1]) || out.Header().Get("Content-Length") != fmt.Sprint(end-start+1) {
				t.Fatalf("inconsistent virtual range %s: %x; want %x", span, out.Body.Bytes(), want[start:end+1])
			}
		})
	}
}

func TestSeekPositionDoesNotJumpBackAfterPassingOffset(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	in, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &nativePlayer{cmd: cmd, in: in, window: &seekWindow{startMS: 60000}}
	s := &Server{player: p, closing: true}
	s.monitor(p, 0, io.NopCloser(strings.NewReader("{\"event\":\"position\",\"position_ms\":80000}\n")))
	if s.snapshot().PositionMS != 140000 {
		t.Fatal("seek offset lost after 80 seconds", s.snapshot().PositionMS)
	}
}

func TestSeekWithoutIndexKeepsCurrentPlayer(t *testing.T) {
	p := &nativePlayer{}
	s := &Server{player: p, state: State{Queue: []Track{{ID: "abcdefghijk"}}, Status: "playing", PositionMS: 15000}}
	r := s.command(context.Background(), request{Action: "seek", PositionMS: 60000})
	if r.Error == "" || s.player != p || s.state.PositionMS != 15000 || s.state.Status != "playing" {
		t.Fatal("unsupported seek interrupted current playback")
	}
}

func TestSeekIndexNetworkErrorDoesNotExposeSignedURL(t *testing.T) {
	c := newCatalog()
	c.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://private.googlevideo.com/?signature=secret")
	})
	_, err := c.seekWindow(context.Background(), streamInfo{url: "https://test.googlevideo.com/audio?signature=secret", initEnd: 631, indexStart: 632, indexEnd: 700, length: 2000}, 60000)
	if err == nil || strings.Contains(err.Error(), "signature") || strings.Contains(err.Error(), "secret") {
		t.Fatal("index failure leaked signed URL", err)
	}
}

func TestParseSidxRejectsUnsupportedOrOverflowingHeader(t *testing.T) {
	b := buildSidx(44100, 0, [][2]uint32{{100, 441000}})
	b[8] = 2
	if _, err := parseSidx(b, 0); err == nil {
		t.Fatal("unsupported index version accepted")
	}
	b[8] = 0
	if _, err := parseSidx(b, 1<<63-10); err == nil {
		t.Fatal("overflowing fragment offset accepted")
	}
}
