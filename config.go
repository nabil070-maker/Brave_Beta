package main

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Repo          string   `yaml:"repo"`
	ReleaseMatch  string   `yaml:"release_match"`
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
		ReleaseMatch:  `(?i)beta`,
		AssetPattern:  `(?i)^BraveBrowserStandaloneBetaSetup\.exe$`,
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
release_match: '(?i)beta'
asset_pattern: '(?i)^BraveBrowserStandaloneBetaSetup\.exe$'

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
	return cfg, err
}
