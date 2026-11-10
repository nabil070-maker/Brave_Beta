package main

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Repo          string   `yaml:"repo"`
	MarkerPattern string   `yaml:"marker_pattern"`
	AssetPattern  string   `yaml:"asset_pattern"`
	CheckUpdates  bool     `yaml:"check_updates"`
	CleanRegistry bool     `yaml:"clean_registry"`
	RegistryKeys  []string `yaml:"registry_keys"`
	Args          []string `yaml:"args"`
}

type Paths struct {
	Root, App, Data, Profile, Cache, Log, Reg, Tmp string
}

func newPaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	root := filepath.Dir(exe)
	data := filepath.Join(root, "data")
	return Paths{
		Root:    root,
		App:     filepath.Join(root, "app"),
		Data:    data,
		Profile: filepath.Join(data, "profile"),
		Cache:   filepath.Join(data, "cache"),
		Log:     filepath.Join(root, "log"),
		Reg:     filepath.Join(root, "reg"),
		Tmp:     filepath.Join(root, "tmp"),
	}, nil
}

func defaultConfig() Config {
	return Config{
		Repo:          "brave/brave-browser",
		MarkerPattern: `(?i)^BraveBrowserStandaloneBetaSetup\.exe$`,
		AssetPattern:  `(?i)^brave-v\d+(\.\d+)+-win32-x64\.zip$`,
		CheckUpdates:  false,
		CleanRegistry: true,
		RegistryKeys:  []string{`HKCU\Software\BraveSoftware\Brave-Browser-Beta`},
		Args: []string{
			"--no-default-browser-check",
			"--disable-machine-id",
		},
	}
}

const sampleYML = `# Brave Beta Portable config
repo: brave/brave-browser
# A release counts as Beta if it contains an asset matching marker_pattern.
marker_pattern: '(?i)^BraveBrowserStandaloneBetaSetup\.exe$'
# The portable zip we download from that release.
asset_pattern: '(?i)^brave-v\d+(\.\d+)+-win32-x64\.zip$'

# Auto-update at launch (when no other instance is running)
check_updates: false

# Export + delete registry keys after exit (no trace on host)
clean_registry: true
registry_keys:
  - HKCU\Software\BraveSoftware\Brave-Browser-Beta

# Extra Brave flags
args:
  - --no-default-browser-check
  - --disable-machine-id
  # Optional, makes saved passwords/cookies readable on other PCs.
  # Less secure (no Windows DPAPI protection). Test it first:
  # - --disable-encryption-win
`

func loadConfig(p Paths) (Config, error) {
	cfg := defaultConfig()
	path := filepath.Join(p.Root, "brave-beta-portable.yml")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_ = os.WriteFile(path, []byte(sampleYML), 0o644)
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	err = yaml.Unmarshal(b, &cfg)
	// Old configs pointed at the installer, which can no longer be extracted.
	if strings.Contains(cfg.AssetPattern, "Standalone") {
		cfg.AssetPattern = defaultConfig().AssetPattern
	}
	return cfg, err
}
