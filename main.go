package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"golang.org/x/sys/windows"
)

var version = "0.3.0"

func msgBox(title, text string, icon uint32) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	windows.MessageBox(0, t, c, windows.MB_OK|icon)
}

func fatal(err error) {
	log.Printf("FATAL: %v", err)
	msgBox("Brave Beta Portable", err.Error(), windows.MB_ICONERROR)
	os.Exit(1)
}

func main() {
	p, err := newPaths()
	if err != nil {
		fatal(err)
	}
	for _, d := range []string{p.Data, p.Profile, p.Cache, p.Log, p.Reg, p.Tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fatal(err)
		}
	}

	lf, err := os.OpenFile(filepath.Join(p.Log, "brave-beta-portable.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		defer lf.Close()
		log.SetOutput(io.MultiWriter(os.Stdout, lf))
	}
	log.SetFlags(log.LstdFlags)

	cfg, err := loadConfig(p)
	if err != nil {
		fatal(fmt.Errorf("config error: %w", err))
	}

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		switch args[0] {
		case "install", "update", "version":
			cmd, args = args[0], args[1:]
		}
	}

	switch cmd {
	case "version":
		fmt.Printf("launcher %s | brave %s\n", version, installedVersion(p))
	case "install":
		if err := ensureLatest(cfg, p, true); err != nil {
			fatal(err)
		}
	case "update":
		if err := ensureLatest(cfg, p, false); err != nil {
			fatal(err)
		}
	default:
		if err := run(cfg, p, args); err != nil {
			fatal(err)
		}
	}
}

var verRe = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)

func installedVersion(p Paths) string {
	ents, err := os.ReadDir(p.App)
	if err != nil {
		return "not installed"
	}
	var vs []string
	for _, e := range ents {
		if e.IsDir() && verRe.MatchString(e.Name()) {
			vs = append(vs, e.Name())
		}
	}
	if len(vs) == 0 {
		return "unknown"
	}
	sort.Strings(vs)
	return vs[len(vs)-1]
}
