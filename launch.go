package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// Returns true if another launcher from this same folder is already running.
func acquireInstance(root string) (already bool, release func()) {
	h := sha1.Sum([]byte(strings.ToLower(root)))
	name, _ := windows.UTF16PtrFromString(`Local\BraveBetaPortable-` + hex.EncodeToString(h[:6]))
	handle, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		return true, func() {}
	}
	if err != nil {
		return false, func() {}
	}
	return false, func() { windows.CloseHandle(handle) }
}

func buildArgs(cfg Config, p Paths, extra []string) []string {
	args := []string{
		"--user-data-dir=" + p.Profile,
		"--disk-cache-dir=" + p.Cache,
	}
	args = append(args, cfg.Args...)
	return append(args, extra...)
}

func run(cfg Config, p Paths, extra []string) error {
	already, release := acquireInstance(p.Root)
	defer release()

	if !already {
		if !isInstalled(p) {
			msgBox("Brave Beta Portable",
				"Brave Beta is not installed yet.\nIt will be downloaded now (about 150 MB). Click OK to start; Brave opens automatically when done.",
				windows.MB_ICONINFORMATION)
			if err := ensureLatest(cfg, p, true); err != nil {
				return err
			}
		} else if cfg.CheckUpdates {
			if err := ensureLatest(cfg, p, false); err != nil {
				log.Printf("update check failed (continuing): %v", err)
			}
		}
	}

	exe := filepath.Join(p.App, "brave.exe")
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("brave.exe not found, run: brave-beta-portable.exe install")
	}

	cmd := exec.Command(exe, buildArgs(cfg, p, extra)...)
	cmd.Dir = p.App

	if already {
		// hand off to the running instance (new window / tab) and exit
		log.Println("already running, opening new window")
		return cmd.Start()
	}

	owned := registryImport(cfg, p)
	log.Printf("launching: %s", cmd.String())
	if err := cmd.Start(); err != nil {
		registryExport(cfg, p, owned)
		return err
	}
	err := cmd.Wait()
	time.Sleep(2 * time.Second) // let child processes flush and exit
	registryExport(cfg, p, owned)
	log.Println("Brave exited")
	return err
}
