package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	if lunaPackageInstalled() {
		return lunaDaemon(action)
	}
	switch action {
	case "start":
		if _, e := call(request{Action: "status"}); e == nil {
			fmt.Println("ytm-alice уже запущен")
			return nil
		}
		executable, e := os.Executable()
		if e != nil {
			return e
		}
		log, e := os.OpenFile("/tmp/ytm-alice.log", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if e != nil {
			return e
		}
		defer log.Close()
		cmd := exec.Command(executable, "serve")
		cmd.Env = os.Environ()
		cmd.Stdout = log
		cmd.Stderr = log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if e = cmd.Start(); e != nil {
			return e
		}
		go cmd.Wait()
		for i := 0; i < 40; i++ {
			if _, e := call(request{Action: "status"}); e == nil {
				fmt.Println("ytm-alice запущен")
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("ytm-alice не запустился: /tmp/ytm-alice.log")
	case "stop":
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
			if err := bundledMedia("install"); err != nil {
				return err
			}
			if err := bundledMedia("start"); err != nil {
				return err
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
