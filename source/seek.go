package main

// Seeking without a GStreamer seek. YouTube audio is fragmented MP4: an init
// segment (ftyp+moov), a sidx index, then moof/mdat fragments. A flushing seek
// in the firmware pipeline over the HTTP relay often ends in EOS, which looked
// like the end of the track. Instead the relay serves a virtual file: the init
// segment followed directly by the fragments from the requested time on, so the
// player simply starts there.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
)

type seekWindow struct {
	head       []byte  // init segment, bytes [0, initEnd]
	fragStart  int64   // real offset of the first served fragment
	startMS    int64   // presentation time of that fragment
	startTicks uint64  // the same time in index (track) timescale units
	frags      []int64 // real offsets of all served fragments
}

func (w *seekWindow) length(src streamInfo) int64 {
	return int64(len(w.head)) + src.length - w.fragStart
}

type sidxRef struct {
	offset     int64 // absolute byte offset of the fragment
	size       int64
	startMS    int64
	durationMS int64
	ticks      uint64 // start time in the index timescale
}

// parseSidx reads an ISO BMFF sidx box located at absolute offset boxStart.
func parseSidx(b []byte, boxStart int64) ([]sidxRef, error) {
	if len(b) < 8 || string(b[4:8]) != "sidx" {
		return nil, errors.New("no sidx box")
	}
	size := int64(binary.BigEndian.Uint32(b))
	if size < 32 || size > int64(len(b)) {
		return nil, errors.New("bad sidx size")
	}
	b = b[:size]
	version := b[8]
	if version > 1 || boxStart < 0 {
		return nil, errors.New("unsupported sidx header")
	}
	p := 12 + 4 // full box header + reference_ID
	timescale := int64(binary.BigEndian.Uint32(b[p:]))
	p += 4
	var ept, first int64
	if version == 0 {
		ept = int64(binary.BigEndian.Uint32(b[p:]))
		first = int64(binary.BigEndian.Uint32(b[p+4:]))
		p += 8
	} else {
		if len(b) < p+16 {
			return nil, errors.New("short sidx")
		}
		ept = int64(binary.BigEndian.Uint64(b[p:]))
		first = int64(binary.BigEndian.Uint64(b[p+8:]))
		p += 16
	}
	if ept < 0 || first < 0 || first > math.MaxInt64-size || boxStart > math.MaxInt64-size-first {
		return nil, errors.New("sidx offset overflow")
	}
	p += 2 // reserved
	if timescale <= 0 || len(b) < p+2 {
		return nil, errors.New("bad sidx header")
	}
	count := int(binary.BigEndian.Uint16(b[p:]))
	p += 2
	if len(b) < p+12*count {
		return nil, errors.New("short sidx references")
	}
	refs := make([]sidxRef, 0, count)
	offset := boxStart + size + first
	t := ept
	for i := 0; i < count; i++ {
		word := binary.BigEndian.Uint32(b[p:])
		dur := int64(binary.BigEndian.Uint32(b[p+4:]))
		p += 12
		if word>>31 != 0 {
			return nil, errors.New("hierarchical sidx is not supported")
		}
		rs := int64(word & 0x7fffffff)
		if rs == 0 || dur == 0 || offset > math.MaxInt64-rs || t > math.MaxInt64/1000-dur {
			return nil, errors.New("invalid sidx reference")
		}
		refs = append(refs, sidxRef{offset: offset, size: rs, startMS: t * 1000 / timescale, durationMS: dur * 1000 / timescale, ticks: uint64(t)})
		offset += rs
		t += dur
	}
	return refs, nil
}

// seekWindow fetches the init segment and the index in one small range request
// and picks the fragment that contains targetMS.
func (c *Catalog) seekWindow(ctx context.Context, src streamInfo, targetMS int64) (*seekWindow, error) {
	if !src.hasSeekIndex() {
		return nil, errors.New("stream has no usable index")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url, nil)
	if err != nil {
		return nil, errors.New("invalid index URL")
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", src.indexEnd))
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("index request: " + streamErrorClass(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, errors.New("index request: HTTP " + strconv.Itoa(resp.StatusCode))
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, src.indexEnd+1))
	if err != nil || int64(len(buf)) != src.indexEnd+1 {
		return nil, errors.New("short index response")
	}
	refs, err := parseSidx(buf[src.indexStart:], src.indexStart)
	if err != nil || len(refs) == 0 {
		return nil, fmt.Errorf("index: %v", err)
	}
	pick := refs[0]
	for _, r := range refs {
		if r.offset <= src.indexEnd || r.offset >= src.length || r.size > src.length-r.offset {
			return nil, errors.New("index points outside the stream")
		}
		if r.startMS > targetMS {
			continue
		}
		pick = r
	}
	if pick.offset <= src.indexEnd || pick.offset >= src.length {
		return nil, errors.New("index points outside the stream")
	}
	win := &seekWindow{head: append([]byte(nil), buf[:src.initEnd+1]...), fragStart: pick.offset, startMS: pick.startMS, startTicks: pick.ticks}
	for _, r := range refs {
		if r.offset >= pick.offset {
			win.frags = append(win.frags, r.offset)
		}
	}
	return win, nil
}

func (src streamInfo) hasSeekIndex() bool {
	return src.initEnd > 0 && src.indexStart == src.initEnd+1 && src.indexEnd > src.indexStart && src.indexEnd < src.length && src.indexEnd <= 1<<20
}

// relayWindow serves the virtual file: init segment from memory, fragments
// from the CDN through relayAudio.
func (s *Server) relayWindow(w http.ResponseWriter, r *http.Request, p *nativePlayer, src streamInfo, win *seekWindow) {
	total := win.length(src)
	start, end, err := byteRange(r.Header.Get("Range"), total)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
		w.WriteHeader(416)
		return
	}
	w.Header().Set("Content-Type", src.mime)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Range") != "" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	head := int64(len(win.head))
	if start < head {
		if _, err := w.Write(win.head[start:min(end+1, head)]); err != nil {
			return
		}
	}
	if end < head {
		return
	}
	realStart := win.fragStart + max(start-head, 0)
	realEnd := win.fragStart + (end - head)
	// A byte range can split a moof (including the tfdt field). Read its whole
	// bounded prefix for timestamp patching, then emit only the requested slice.
	fetchStart, fetchEnd := realStart, realEnd
	for i, f := range win.frags {
		if f <= realStart && realStart-f < maxMoof {
			fetchStart = f
		}
		if f <= realEnd && realEnd-f < maxMoof {
			last := src.length - 1
			if i+1 < len(win.frags) {
				last = win.frags[i+1] - 1
			}
			fetchEnd = min(f+maxMoof-1, last)
		}
	}
	slice := &tfdtSliceWriter{ResponseWriter: w, skip: realStart - fetchStart, remaining: realEnd - realStart + 1}
	pw := &tfdtWriter{ResponseWriter: slice, pos: fetchStart, frags: win.frags, shift: win.startTicks}
	s.relayAudioRange(pw, r, p, src, fetchStart, fetchEnd, true)
	pw.flush()
}

type tfdtSliceWriter struct {
	http.ResponseWriter
	skip, remaining int64
}

func (w *tfdtSliceWriter) Write(p []byte) (int, error) {
	n := len(p)
	drop := min(w.skip, int64(n))
	w.skip -= drop
	p = p[drop:]
	keep := min(w.remaining, int64(len(p)))
	if keep > 0 {
		written, err := w.ResponseWriter.Write(p[:keep])
		w.remaining -= int64(written)
		if err != nil {
			return 0, err
		}
		if int64(written) != keep {
			return 0, io.ErrShortWrite
		}
	}
	return n, nil
}

// tfdtWriter shifts baseMediaDecodeTime in every served fragment so the virtual
// file starts at time zero. Otherwise the decoder outputs the first fragment at
// its original time and the sink waits that long in silence.
type tfdtWriter struct {
	http.ResponseWriter
	pos     int64   // real offset of the next byte written
	frags   []int64 // fragment offsets, ascending
	shift   uint64
	buf     []byte // a moof being collected
	collect bool
}

const maxMoof = 256 << 10

func (t *tfdtWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if t.collect {
			t.buf = append(t.buf, p...)
			p = nil
			if len(t.buf) < 8 {
				break
			}
			size := int(binary.BigEndian.Uint32(t.buf))
			if string(t.buf[4:8]) != "moof" || size < 8 || size > maxMoof {
				if err := t.flush(); err != nil {
					return 0, err
				}
				break
			}
			if len(t.buf) < size {
				break
			}
			patchTfdt(t.buf[:size], t.shift)
			rest := append([]byte(nil), t.buf[size:]...)
			t.buf = t.buf[:size]
			if err := t.flush(); err != nil {
				return 0, err
			}
			p = rest
			continue
		}
		next := int64(-1)
		for _, f := range t.frags {
			if f >= t.pos {
				next = f
				break
			}
		}
		if next < 0 || next >= t.pos+int64(len(p)) {
			if _, err := t.ResponseWriter.Write(p); err != nil {
				return 0, err
			}
			t.pos += int64(len(p))
			break
		}
		lead := next - t.pos
		if lead > 0 {
			if _, err := t.ResponseWriter.Write(p[:lead]); err != nil {
				return 0, err
			}
			t.pos += lead
			p = p[lead:]
		}
		t.collect = true
		t.buf = t.buf[:0]
	}
	return n, nil
}

// flush writes a pending (possibly unpatched) buffer.
func (t *tfdtWriter) flush() error {
	t.collect = false
	if len(t.buf) == 0 {
		return nil
	}
	_, err := t.ResponseWriter.Write(t.buf)
	t.pos += int64(len(t.buf))
	t.buf = t.buf[:0]
	return err
}

// patchTfdt subtracts shift from baseMediaDecodeTime in moof/traf/tfdt boxes.
func patchTfdt(moof []byte, shift uint64) {
	var walk func(b []byte)
	walk = func(b []byte) {
		for len(b) >= 8 {
			size := int(binary.BigEndian.Uint32(b))
			if size < 8 || size > len(b) {
				return
			}
			box := b[:size]
			switch string(box[4:8]) {
			case "traf":
				walk(box[8:])
			case "tfdt":
				if size >= 16 && box[8] == 0 {
					v := uint64(binary.BigEndian.Uint32(box[12:]))
					binary.BigEndian.PutUint32(box[12:], uint32(v-min(v, shift)))
				} else if size >= 20 && box[8] == 1 {
					v := binary.BigEndian.Uint64(box[12:])
					binary.BigEndian.PutUint64(box[12:], v-min(v, shift))
				}
			}
			b = b[size:]
		}
	}
	if len(moof) >= 8 {
		walk(moof[8:])
	}
}
