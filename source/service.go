package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"
)

func serve() error {
	debug.SetMemoryLimit(48 << 20)
	debug.SetGCPercent(50)
	if e := os.MkdirAll(dataDir(), 0700); e != nil {
		return e
	}
	if e := os.MkdirAll(runDir(), 0700); e != nil {
		return e
	}
	// An existing live socket belongs to another service instance.
	if c, e := net.DialTimeout("unix", socketPath(), time.Second); e == nil {
		c.Close()
		return errors.New("ytm-alice уже запущен")
	}
	os.Remove(socketPath())
	listener, e := net.Listen("unix", socketPath())
	if e != nil {
		return e
	}
	defer listener.Close()
	defer os.Remove(socketPath())
	os.Chmod(socketPath(), 0600)
	os.WriteFile(filepath.Join(runDir(), "pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
	defer os.Remove(filepath.Join(runDir(), "pid"))
	s := &Server{state: State{Version: version, Status: "stopped", Announce: true, Queue: []Track{}}, catalog: newCatalog()}
	s.luna, e = newLunaClient()
	if e != nil {
		return e
	}
	if err := s.catalog.loadAuth(); err != nil {
		log.Print(err)
	}
	if raw, e := os.ReadFile(filepath.Join(dataDir(), "state.json")); e == nil {
		json.Unmarshal(raw, &s.state)
		s.state.Version = version
		s.state.Status = "stopped"
		s.state.PositionMS = 0
		s.state.Error = ""
		if s.state.Index < 0 || s.state.Index >= len(s.state.Queue) {
			s.state.Index = 0
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	closeWeb, webErr := s.startWeb()
	if webErr != nil {
		log.Print("web panel unavailable: ", webErr)
	} else {
		defer closeWeb()
	}
	httpServer := &http.Server{Addr: relayAddr, Handler: http.HandlerFunc(s.relay), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	if s.luna != nil {
		go s.lunaLoop(ctx)
	}
	// Retain the proven station speech adapter while Luna coordinates media.
	// Its cancellation protocol differs across station firmware families.
	go s.voiceLoop(ctx)
	go s.mediaLoop(ctx)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.controlConnection(ctx, conn)
		}
	}()
	log.Print("ytm-alice ", version, " ready; native Alice, local control, direct YouTube")
	select {
	case <-ctx.Done():
	case e = <-errCh:
	}
	s.mu.Lock()
	s.closing = true
	s.stopLocked()
	s.state.Status = "stopped"
	s.saveLocked()
	s.mu.Unlock()
	cleanup, done := context.WithTimeout(context.Background(), 4*time.Second)
	_ = s.waitPlayback(cleanup, "paused")
	done()
	httpServer.Close()
	return e
}
