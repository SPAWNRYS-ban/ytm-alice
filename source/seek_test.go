package main

import (
	"bytes"
	"encoding/binary"
	"net/http/httptest"
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
