package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const bootPath = "/etc/init.d/S99zytm-alice"
const bootMarker = "ytm-alice managed boot hook"

func packageRoot() string {
	if s := os.Getenv("YAM_ROOT"); s != "" {
		return s
	}
	return "/data/yam"
}

func bundledMedia(action string) error {
	cmd := exec.Command(filepath.Join(packageRoot(), "bin", "alice-media"), action)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("встроенный медиаслой (%s): %w", action, err)
	}
	return nil
}

func daemon(action string) error {
	switch action {
	case "start":
		if _, e := call(request{Action: "status"}); e == nil {
			fmt.Println("ytm-alice уже запущен")
			return nil
		}
		// A live supervisor restarts serve by itself; a second one would race it for the socket.
		if _, alive := ownPID("supervisor.pid"); !alive {
			executable, e := os.Executable()
			if e != nil {
				return e
			}
			logFile, e := os.OpenFile("/tmp/ytm-alice.log", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
			if e != nil {
				return e
			}
			defer logFile.Close()
			cmd := exec.Command(executable, "supervise")
			cmd.Env = os.Environ()
			cmd.Stdout = logFile
			cmd.Stderr = logFile
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if e = cmd.Start(); e != nil {
				return e
			}
			go cmd.Wait()
		}
		for i := 0; i < 40; i++ {
			if _, e := call(request{Action: "status"}); e == nil {
				fmt.Println("ytm-alice запущен")
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("ytm-alice не запустился: /tmp/ytm-alice.log")
	case "stop":
		// Stop the supervisor first, otherwise it restarts serve. It forwards
		// SIGTERM to serve and removes its PID file after serve has exited.
		if pid, alive := ownPID("supervisor.pid"); alive {
			if e := syscall.Kill(pid, syscall.SIGTERM); e != nil {
				return e
			}
			for i := 0; i < 60; i++ {
				if _, alive := ownPID("supervisor.pid"); !alive {
					fmt.Println("ytm-alice остановлен")
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return errors.New("служба не завершилась за 6 секунд")
		}
		if _, e := call(request{Action: "status"}); e != nil {
			return nil
		}
		raw, e := os.ReadFile(filepath.Join(runDir(), "pid"))
		if e != nil {
			return e
		}
		pid, e := strconv.Atoi(strings.TrimSpace(string(raw)))
		if e != nil || pid <= 1 {
			return errors.New("неверный PID службы")
		}
		target, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if e != nil {
			return e
		}
		if filepath.Base(strings.TrimSuffix(target, " (deleted)")) != "ytm-alice" {
			return errors.New("PID принадлежит другой программе")
		}
		if e = syscall.Kill(pid, syscall.SIGTERM); e != nil {
			return e
		}
		for i := 0; i < 40; i++ {
			if _, e := os.Stat(filepath.Join(runDir(), "pid")); os.IsNotExist(e) {
				fmt.Println("ytm-alice остановлен")
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("служба не завершилась за 4 секунды")
	case "restart":
		if e := daemon("stop"); e != nil {
			return e
		}
		return daemon("start")
	case "install", "uninstall":
		if os.Geteuid() != 0 {
			return errors.New("для настройки автозапуска нужен root")
		}
		if old, e := os.ReadFile(bootPath); e == nil && !strings.Contains(string(old), bootMarker) {
			return errors.New("путь автозапуска занят чужим файлом")
		}
		if action == "uninstall" {
			if err := daemon("stop"); err != nil {
				return err
			}
			if err := bundledMedia("uninstall"); err != nil {
				return err
			}
		} else {
			// The Bluetooth media layer is optional: a firmware it does not
			// fit must still get ytm-alice's own autostart below.
			if err := bundledMedia("install"); err != nil {
				fmt.Fprintln(os.Stderr, "Предупреждение:", err)
			} else if err := bundledMedia("start"); err != nil {
				fmt.Fprintln(os.Stderr, "Предупреждение:", err)
			}
		}
		root := packageRoot()
		if strings.ContainsAny(root, "'\n\r") {
			return errors.New("недопустимый путь YAM_ROOT")
		}
		if e := exec.Command("mount", "-o", "remount,rw", "/").Run(); e != nil {
			return e
		}
		err := func() error {
			if action == "uninstall" {
				if dest, e := os.Readlink("/usr/bin/ytm-alice"); e == nil && dest == filepath.Join(root, "bin", "ytm-alice") {
					os.Remove("/usr/bin/ytm-alice")
				}
				e := os.Remove(bootPath)
				if os.IsNotExist(e) {
					return nil
				}
				return e
			}
			link := filepath.Join(root, "bin", "ytm-alice")
			if _, e := os.Lstat("/usr/bin/ytm-alice"); os.IsNotExist(e) {
				if e = os.Symlink(link, "/usr/bin/ytm-alice"); e != nil {
					return e
				}
			} else if dest, e := os.Readlink("/usr/bin/ytm-alice"); e != nil || dest != link {
				return errors.New("/usr/bin/ytm-alice принадлежит другой программе")
			}
			script := "#!/bin/sh\n# " + bootMarker + "\nexport YAM_ROOT='" + root + "'\nexport YAM_VAR=\"$YAM_ROOT/var\"\ncase \"${1:-}\" in\n start) [ -x \"$YAM_ROOT/bin/ytm-alice\" ] && \"$YAM_ROOT/bin/ytm-alice\" daemon start ;;\n stop) [ -x \"$YAM_ROOT/bin/ytm-alice\" ] && \"$YAM_ROOT/bin/ytm-alice\" daemon stop ;;\nesac\nexit 0\n"
			return os.WriteFile(bootPath, []byte(script), 0755)
		}()
		exec.Command("sync").Run()
		restoreErr := exec.Command("mount", "-o", "remount,ro", "/").Run()
		if err != nil {
			return err
		}
		if restoreErr != nil {
			return restoreErr
		}
		if action == "uninstall" {
			return nil
		}
		return daemon("start")
	}
	return errors.New("неизвестная команда daemon")
}

// ownPID reads a PID file in runDir and reports whether it names a live
// ytm-alice process, so a stale file never makes us signal another program.
func ownPID(name string) (int, bool) {
	raw, e := os.ReadFile(filepath.Join(runDir(), name))
	if e != nil {
		return 0, false
	}
	pid, e := strconv.Atoi(strings.TrimSpace(string(raw)))
	if e != nil || pid <= 1 {
		return 0, false
	}
	target, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if e != nil || filepath.Base(strings.TrimSuffix(target, " (deleted)")) != "ytm-alice" {
		return 0, false
	}
	return pid, true
}

// supervise keeps serve running: the firmware has no service manager and
// YAM only stores service declarations, so a crash would otherwise leave
// the Station without YouTube Music until reboot. Same approach as the
// bt-proxy loop in alice-media. A serve that dies quickly is restarted with
// a growing delay so a broken config does not spin the CPU.
func supervise() error {
	if e := os.MkdirAll(runDir(), 0700); e != nil {
		return e
	}
	pidFile := filepath.Join(runDir(), "supervisor.pid")
	if e := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0600); e != nil {
		return e
	}
	defer os.Remove(pidFile)
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	delay := 2 * time.Second
	for {
		cmd := exec.Command(executable, "serve")
		cmd.Env = os.Environ()
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		started := time.Now()
		if e := cmd.Start(); e != nil {
			return e
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-stop:
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
			}
			return nil
		case err := <-done:
			if time.Since(started) > time.Minute {
				delay = 2 * time.Second
			}
			log.Printf("supervisor: serve exited (%v), restarting in %s", err, delay)
			select {
			case <-stop:
				return nil
			case <-time.After(delay):
			}
			if delay < time.Minute {
				delay *= 2
			}
		}
	}
}
