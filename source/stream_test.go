package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func streamFixture(data []byte) (*Server, *nativePlayer) {
	c := newCatalog()
	p := &nativePlayer{token: "test", source: streamInfo{url: "https://example.googlevideo.com/audio?signature=private", mime: "audio/mp4", length: int64(len(data))}}
	return &Server{catalog: c, player: p}, p
}
func streamSpan(t *testing.T, r *http.Request) (int64, int64) {
	t.Helper()
	var start, end int64
	if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
		t.Fatal(err)
	}
	return start, end
}
func streamResponse(data []byte, start, end int64, total int) *http.Response {
	return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, total)}}, Body: io.NopCloser(bytes.NewReader(data))}
}

func TestStreamResumesOnlyMissingBytesAfterTruncatedBody(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefg"), 2000)
	s, _ := streamFixture(data)
	calls := 0
	s.catalog.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		start, end := streamSpan(t, r)
		if calls == 1 {
			return streamResponse(data[:123], start, end, len(data)), nil
		}
		if start != 123 {
			t.Fatalf("replayed bytes after interruption: start=%d", start)
		}
		return streamResponse(data[start:end+1], start, end, len(data)), nil
	})}
	out := httptest.NewRecorder()
	s.relay(out, httptest.NewRequest("GET", "http://127.0.0.1/audio/test", nil))
	if calls != 2 || out.Code != 200 || !bytes.Equal(out.Body.Bytes(), data) {
		t.Fatal("truncated stream was not resumed without duplication")
	}
}

func TestStreamSwitchesToFreshCompatibleURLAfterFailure(t *testing.T) {
	data := []byte("complete audio")
	s, p := streamFixture(data)
	p.source.id = "abcdefghijk"
	old := p.source
	fresh := old
	fresh.url = "https://other.googlevideo.com/audio?signature=new-private"
	fresh.until = time.Now().Add(time.Hour)
	s.catalog.streams[old.id] = old
	calls := 0
	s.catalog.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			// A concurrent resolve has obtained a fresh URL. An older failure must
			// not evict it; the retry must pick it up rather than reuse the stale URL.
			s.catalog.mu.Lock()
			s.catalog.streams[old.id] = fresh
			s.catalog.mu.Unlock()
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if r.URL.String() != fresh.url {
			t.Fatal("retry reused the failed URL")
		}
		start, end := streamSpan(t, r)
		return streamResponse(data[start:end+1], start, end, len(data)), nil
	})}
	out := httptest.NewRecorder()
	s.relay(out, httptest.NewRequest("GET", "http://127.0.0.1/audio/test", nil))
	if calls != 2 || !bytes.Equal(out.Body.Bytes(), data) || p.source.url != fresh.url {
		t.Fatal("fresh stream was not used")
	}
	s.catalog.discardStream(old)
	if s.catalog.streams[old.id].url != fresh.url {
		t.Fatal("an older failure evicted a newer URL")
	}
	s.catalog.discardStream(fresh)
	if _, ok := s.catalog.streams[old.id]; ok {
		t.Fatal("failed current stream remained cached")
	}
}

func TestStreamRetryBudgetAndLogsDoNotExposeSignedURL(t *testing.T) {
	s, _ := streamFixture([]byte("audio"))
	calls := 0
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	s.catalog.http = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("net/http: timeout awaiting response headers: https://private.googlevideo.com/?signature=secret-value")
	})}
	out := httptest.NewRecorder()
	s.relay(out, httptest.NewRequest("GET", "http://127.0.0.1/audio/test", nil))
	if calls != streamAttempts || out.Code != 502 {
		t.Fatalf("retry budget not respected: %d, %d", calls, out.Code)
	}
	if strings.Contains(logs.String(), "secret-value") || strings.Contains(logs.String(), "signature=") || !strings.Contains(logs.String(), "reason=response-header-timeout") {
		t.Fatal("network diagnostics either lost the reason or leaked the URL")
	}
}

func TestStreamCancellationDoesNotRetryOrLogNetworkFailure(t *testing.T) {
	s, _ := streamFixture([]byte("audio"))
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	s.catalog.http = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { calls++; cancel(); return nil, context.Canceled })}
	out := httptest.NewRecorder()
	s.relay(out, httptest.NewRequest("GET", "http://127.0.0.1/audio/test", nil).WithContext(ctx))
	if calls != 1 || logs.Len() != 0 {
		t.Fatal("stopping playback caused a spurious retry or network error")
	}
}

type failedAudioWriter struct{ header http.Header }

func (w *failedAudioWriter) Header() http.Header       { return w.header }
func (w *failedAudioWriter) WriteHeader(int)           {}
func (w *failedAudioWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestStreamDownstreamDisconnectDoesNotDownloadAgain(t *testing.T) {
	data := []byte("audio")
	s, _ := streamFixture(data)
	calls := 0
	s.catalog.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		start, end := streamSpan(t, r)
		return streamResponse(data, start, end, len(data)), nil
	})}
	s.relay(&failedAudioWriter{make(http.Header)}, httptest.NewRequest("GET", "http://127.0.0.1/audio/test", nil))
	if calls != 1 {
		t.Fatal("closed decoder triggered an upstream retry")
	}
}

func TestStreamRejectsWrongRangeBeforeSendingAudio(t *testing.T) {
	data := []byte("audio")
	s, _ := streamFixture(data)
	calls := 0
	s.catalog.http = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { calls++; return streamResponse(data, 1, 5, 6), nil })}
	out := httptest.NewRecorder()
	s.relay(out, httptest.NewRequest("GET", "http://127.0.0.1/audio/test", nil))
	if calls != 1 || out.Code != 502 || out.Body.Len() != 0 {
		t.Fatal("wrong stream position reached the decoder")
	}
}
