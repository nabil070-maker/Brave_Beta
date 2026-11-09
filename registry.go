package main

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"syscall"
)

func reg(args ...string) error {
	c := exec.Command("reg.exe", args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reg %v: %v: %s", args, err, out)
	}
	return nil
}

func regFile(p Paths, i int) string {
	return filepath.Join(p.Reg, fmt.Sprintf("key%d.reg", i))
}

func regExists(key string) bool { return reg("query", key) == nil }

// Returns the indexes of keys we own (did not exist on the host before launch).
func registryImport(cfg Config, p Paths) map[int]bool {
	owned := map[int]bool{}
	for i, k := range cfg.RegistryKeys {
		if regExists(k) {
			log.Printf("registry key already on host, leaving alone: %s", k)
			continue
		}
		owned[i] = true
		if err := reg("import", regFile(p, i)); err != nil {
			log.Printf("registry import skipped: %v", err)
		}
	}
	return owned
}

func registryExport(cfg Config, p Paths, owned map[int]bool) {
	for i, k := range cfg.RegistryKeys {
		if !owned[i] {
			continue
		}
		if err := reg("export", k, regFile(p, i), "/y"); err != nil {
			log.Printf("registry export: %v", err)
			continue
		}
		if cfg.CleanRegistry {
			if err := reg("delete", k, "/f"); err != nil {
				log.Printf("registry delete: %v", err)
			}
		}
	}
}
