package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const streamAttempts = 3

func (c *Catalog) discardStream(src streamInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.streams[src.id]; ok && cached.url == src.url {
		delete(c.streams, src.id)
	}
}

// Signed URLs and underlying error strings must never reach the log.
func streamErrorClass(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if strings.Contains(err.Error(), "timeout awaiting response headers") {
		return "response-header-timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "tls-certificate"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "network-timeout"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return "short-read"
	}
	return "network-io"
}

func streamLog(src streamInfo, offset, last int64, attempt int, reason string) {
	host := "unknown"
	if u, err := url.Parse(src.url); err == nil {
		host = u.Hostname()
	}
	log.Printf("CDN track=%s host=%s range=%d-%d attempt=%d/%d reason=%s", src.id, host, offset, last, attempt, streamAttempts, reason)
}

type streamWriter struct {
	io.Writer
	err error
}

func (w *streamWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func waitStreamRetry(ctx context.Context, attempt int) bool {
	timer := time.NewTimer(time.Duration(attempt) * 200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) relayAudio(w http.ResponseWriter, r *http.Request, p *nativePlayer, src streamInfo, start, end int64) {
	client := *s.catalog.http
	client.Timeout = 0 // A paused reader must not expire the upstream body.
	if transport, ok := client.Transport.(*http.Transport); ok {
		copy := transport.Clone()
		copy.ResponseHeaderTimeout = 5 * time.Second
		copy.TLSHandshakeTimeout = 5 * time.Second
		copy.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		client.Transport = copy
		defer copy.CloseIdleConnections()
	}
	first := true
	fail := func() {
		if first {
			w.WriteHeader(http.StatusBadGateway)
		}
	}
	// Retry only the missing bytes. Data already sent to the decoder is never
	// replayed; downstream cancellation/failed writes never restart a request.
	for offset := start; offset <= end; {
		last := min(offset+(1<<20)-1, end)
		failures := 0
		for offset <= last {
			if r.Context().Err() != nil {
				return
			}
			if failures > 0 {
				if !waitStreamRetry(r.Context(), failures) {
					return
				}
				if failures == 1 && src.id != "" {
					s.catalog.discardStream(src)
					fresh, err := s.catalog.resolve(r.Context(), src.id)
					if r.Context().Err() != nil {
						return
					}
					if err == nil && fresh.length == src.length && fresh.mime == src.mime {
						s.mu.Lock()
						if s.player == p && p.source.url == src.url {
							p.source = fresh
						}
						s.mu.Unlock()
						src = fresh
					} else {
						streamLog(src, offset, last, failures+1, "refresh-unavailable")
					}
				}
			}
			req, err := http.NewRequestWithContext(r.Context(), "GET", src.url, nil)
			if err != nil {
				fail()
				return
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, last))
			req.Header.Set("Accept-Encoding", "identity")
			resp, err := client.Do(req)
			if r.Context().Err() != nil {
				if resp != nil {
					resp.Body.Close()
				}
				return
			}
			retry := false
			if err != nil {
				streamLog(src, offset, last, failures+1, streamErrorClass(err))
				retry = true
			} else if resp.StatusCode != http.StatusPartialContent {
				status := resp.StatusCode
				resp.Body.Close()
				streamLog(src, offset, last, failures+1, "http-"+strconv.Itoa(status))
				retry = status == 403 || status == 408 || status == 410 || status == 429 || status >= 500
				if !retry {
					fail()
					return
				}
			} else {
				// Reject a server that returns a different position/file on retry.
				if span := resp.Header.Get("Content-Range"); span != "" && span != fmt.Sprintf("bytes %d-%d/%d", offset, last, src.length) {
					resp.Body.Close()
					streamLog(src, offset, last, failures+1, "wrong-content-range")
					fail()
					return
				}
				if first {
					w.Header().Set("Content-Type", src.mime)
					w.Header().Set("Accept-Ranges", "bytes")
					w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
					w.Header().Set("Cache-Control", "no-store")
					if r.Header.Get("Range") != "" {
						w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, src.length))
						w.WriteHeader(206)
					} else {
						w.WriteHeader(200)
					}
					first = false
				}
				writer := &streamWriter{Writer: w}
				n, readErr := io.CopyN(writer, resp.Body, last-offset+1)
				resp.Body.Close()
				offset += n
				if writer.err != nil || r.Context().Err() != nil {
					return
				}
				if readErr == nil {
					continue
				}
				streamLog(src, offset, last, failures+1, streamErrorClass(readErr))
				retry = true
			}
			if retry {
				failures++
				if failures >= streamAttempts {
					s.catalog.discardStream(src)
					fail()
					return
				}
			}
		}
	}
}
