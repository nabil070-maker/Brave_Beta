package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bodgit/sevenzip"
)

type ghAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Name    string    `json:"name"`
	Draft   bool      `json:"draft"`
	Assets  []ghAsset `json:"assets"`
}

func httpGet(url string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "brave-beta-portable/"+version)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

func latestRelease(cfg Config) (*ghRelease, *ghAsset, error) {
	relRe, err := regexp.Compile(cfg.ReleaseMatch)
	if err != nil {
		return nil, nil, err
	}
	assetRe, err := regexp.Compile(cfg.AssetPattern)
	if err != nil {
		return nil, nil, err
	}
	resp, err := httpGet(fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=40", cfg.Repo))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, nil, err
	}
	var seen []string
	for i := range rels {
		r := &rels[i]
		if r.Draft || !(relRe.MatchString(r.Name) || relRe.MatchString(r.TagName)) {
			continue
		}
		for j := range r.Assets {
			a := &r.Assets[j]
			seen = append(seen, a.Name)
			if assetRe.MatchString(a.Name) {
				return r, a, nil
			}
		}
		// the newest matching release had no matching asset: report it
		break
	}
	return nil, nil, fmt.Errorf("no matching Beta asset found. Assets seen: %v. Adjust asset_pattern in the yml", seen)
}

func stateFile(p Paths) string { return filepath.Join(p.Root, "installed.txt") }

func installedTag(p Paths) string {
	b, err := os.ReadFile(stateFile(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func isInstalled(p Paths) bool {
	_, err := os.Stat(filepath.Join(p.App, "brave.exe"))
	return err == nil
}

func ensureLatest(cfg Config, p Paths, force bool) error {
	rel, asset, err := latestRelease(cfg)
	if err != nil {
		return err
	}
	if !force && isInstalled(p) && installedTag(p) == rel.TagName {
		log.Printf("already up to date (%s)", rel.TagName)
		return nil
	}
	log.Printf("installing %s (%s)", rel.TagName, asset.Name)

	installer := filepath.Join(p.Tmp, asset.Name)
	sum, err := download(asset.URL, installer)
	if err != nil {
		return err
	}
	if strings.HasPrefix(asset.Digest, "sha256:") {
		want := strings.TrimPrefix(asset.Digest, "sha256:")
		if !strings.EqualFold(want, sum) {
			os.Remove(installer)
			return fmt.Errorf("checksum mismatch: want %s got %s", want, sum)
		}
		log.Println("sha256 verified")
	} else {
		log.Printf("no digest published, sha256=%s", sum)
	}

	stage := p.App + ".new"
	os.RemoveAll(stage)
	if err := extractInstaller(installer, stage); err != nil {
		os.RemoveAll(stage)
		return err
	}
	if _, err := os.Stat(filepath.Join(stage, "brave.exe")); err != nil {
		os.RemoveAll(stage)
		return errors.New("extraction finished but brave.exe is missing")
	}

	old := p.App + ".old"
	os.RemoveAll(old)
	if _, err := os.Stat(p.App); err == nil {
		if err := os.Rename(p.App, old); err != nil {
			os.RemoveAll(stage)
			return fmt.Errorf("could not replace app folder, close Brave first: %w", err)
		}
	}
	if err := os.Rename(stage, p.App); err != nil {
		return err
	}
	os.RemoveAll(old)
	os.Remove(installer)
	if err := os.WriteFile(stateFile(p), []byte(rel.TagName), 0o644); err != nil {
		return err
	}
	log.Printf("installed %s, Brave %s", rel.TagName, installedVersion(p))
	return nil
}

func download(url, dest string) (string, error) {
	resp, err := httpGet(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	pr := &progress{total: resp.ContentLength, last: time.Now()}
	if _, err := io.Copy(io.MultiWriter(f, h, pr), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type progress struct {
	total, done int64
	last        time.Time
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if time.Since(p.last) > 3*time.Second {
		p.last = time.Now()
		if p.total > 0 {
			log.Printf("downloaded %d%%", p.done*100/p.total)
		}
	}
	return len(b), nil
}

// The standalone installer is a PE file with chrome.7z embedded as a resource.
// We find the 7z signature inside it and read the archive directly.
var sevenZipSig = []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}

func extractInstaller(installer, stage string) error {
	data, err := os.ReadFile(installer)
	if err != nil {
		return err
	}
	start := 0
	for {
		i := bytes.Index(data[start:], sevenZipSig)
		if i < 0 {
			return errors.New("chrome.7z not found inside installer")
		}
		off := start + i
		err := extractBlob(data[off:], stage)
		if err == nil {
			return nil
		}
		log.Printf("candidate at offset %d failed: %v", off, err)
		os.RemoveAll(stage)
		start = off + 1
	}
}

func extractBlob(blob []byte, stage string) error {
	r, err := sevenzip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		return err
	}
	cleanStage := filepath.Clean(stage) + string(os.PathSeparator)
	found := false
	for _, f := range r.File {
		name := filepath.ToSlash(f.Name)
		if !strings.HasPrefix(name, "Chrome-bin/") {
			continue
		}
		rel := strings.TrimPrefix(name, "Chrome-bin/")
		if rel == "" {
			continue
		}
		target := filepath.Join(stage, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, cleanStage) {
			return fmt.Errorf("unsafe path in archive: %q", name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeEntry(f, target); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("archive has no Chrome-bin folder")
	}
	return nil
}

func writeEntry(f *sevenzip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}
