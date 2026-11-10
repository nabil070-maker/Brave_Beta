package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
	if strings.Contains(url, "api.github.com") {
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
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

// Returns the zip asset if the release is a Beta release that has it.
func matchRelease(markerRe, assetRe *regexp.Regexp, r *ghRelease) *ghAsset {
	marker := false
	var zipAsset *ghAsset
	for j := range r.Assets {
		a := &r.Assets[j]
		if markerRe.MatchString(a.Name) {
			marker = true
		}
		if assetRe.MatchString(a.Name) {
			zipAsset = a
		}
	}
	if marker {
		return zipAsset
	}
	return nil
}

func apiReleases(cfg Config) ([]ghRelease, error) {
	resp, err := httpGet(fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=40", cfg.Repo))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, err
	}
	return rels, nil
}

var (
	tagRe       = regexp.MustCompile(`releases/tag/([^"<\s]+)`)
	assetLinkRe = regexp.MustCompile(`releases/download/[^"/]+/([^"?]+)"`)
)

func readAll(u string) (string, error) {
	resp, err := httpGet(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), err
}

// Fallback that avoids the rate-limited API: release feed + asset listing pages.
func webRelease(cfg Config, markerRe, assetRe *regexp.Regexp) (*ghRelease, *ghAsset, error) {
	feed, err := readAll(fmt.Sprintf("https://github.com/%s/releases.atom", cfg.Repo))
	if err != nil {
		return nil, nil, err
	}
	var tags []string
	seenTag := map[string]bool{}
	for _, m := range tagRe.FindAllStringSubmatch(feed, -1) {
		if !seenTag[m[1]] {
			seenTag[m[1]] = true
			tags = append(tags, m[1])
		}
	}
	if len(tags) > 25 {
		tags = tags[:25]
	}
	for _, tag := range tags {
		page, err := readAll(fmt.Sprintf("https://github.com/%s/releases/expanded_assets/%s", cfg.Repo, tag))
		if err != nil {
			log.Printf("skip %s: %v", tag, err)
			continue
		}
		rel := &ghRelease{TagName: tag}
		seenAsset := map[string]bool{}
		for _, m := range assetLinkRe.FindAllStringSubmatch(page, -1) {
			name, err := url.PathUnescape(m[1])
			if err != nil || seenAsset[name] {
				continue
			}
			seenAsset[name] = true
			rel.Assets = append(rel.Assets, ghAsset{
				Name: name,
				URL:  fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", cfg.Repo, tag, m[1]),
			})
		}
		if a := matchRelease(markerRe, assetRe, rel); a != nil {
			return rel, a, nil
		}
	}
	return nil, nil, fmt.Errorf("no Beta zip found among %d recent releases. Adjust marker_pattern/asset_pattern in the yml", len(tags))
}

// Finds the newest Beta release that has the portable zip.
func latestRelease(cfg Config) (*ghRelease, *ghAsset, error) {
	markerRe, err := regexp.Compile(cfg.MarkerPattern)
	if err != nil {
		return nil, nil, err
	}
	assetRe, err := regexp.Compile(cfg.AssetPattern)
	if err != nil {
		return nil, nil, err
	}
	rels, err := apiReleases(cfg)
	if err == nil {
		for i := range rels {
			if rels[i].Draft {
				continue
			}
			if a := matchRelease(markerRe, assetRe, &rels[i]); a != nil {
				return &rels[i], a, nil
			}
		}
		err = errors.New("no Beta zip in API results")
	}
	log.Printf("GitHub API unavailable (%v), using web fallback", err)
	return webRelease(cfg, markerRe, assetRe)
}

func expectedSum(rel *ghRelease, a *ghAsset) string {
	if strings.HasPrefix(a.Digest, "sha256:") {
		return strings.TrimPrefix(a.Digest, "sha256:")
	}
	for _, s := range rel.Assets {
		if s.Name == a.Name+".sha256" {
			resp, err := httpGet(s.URL)
			if err != nil {
				return ""
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if f := strings.Fields(string(b)); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
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

var errCancelled = errors.New("installation cancelled")

// Reporter receives progress while installing: the setup window, or the log.
type Reporter interface {
	Phase(text string)
	Progress(pct int, detail string)
	Cancelled() bool
}

type logReporter struct{ last time.Time }

func (l *logReporter) Phase(s string) { log.Println(s) }
func (l *logReporter) Progress(pct int, detail string) {
	if time.Since(l.last) > 3*time.Second {
		l.last = time.Now()
		log.Printf("%d%% %s", pct, detail)
	}
}
func (l *logReporter) Cancelled() bool { return false }

func fmtBytes(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

func fmtDur(sec float64) string {
	s := int(sec + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func ensureLatest(cfg Config, p Paths, force bool) error {
	return ensureLatestR(cfg, p, force, &logReporter{})
}

// Overall progress is split across phases: 0-2 lookup, 2-80 download,
// 80-82 verify, 82-98 extract, 98-100 finish.
func ensureLatestR(cfg Config, p Paths, force bool, rep Reporter) error {
	rep.Phase("Checking for the latest Brave Beta…")
	rep.Progress(1, "")
	rel, asset, err := latestRelease(cfg)
	if err != nil {
		return err
	}
	if !force && isInstalled(p) && installedTag(p) == rel.TagName {
		log.Printf("already up to date (%s)", rel.TagName)
		return nil
	}
	if rep.Cancelled() {
		return errCancelled
	}
	version := strings.TrimPrefix(rel.TagName, "v")
	log.Printf("installing %s (%s)", rel.TagName, asset.Name)

	// clear leftovers from earlier attempts
	os.RemoveAll(p.Tmp)
	if err := os.MkdirAll(p.Tmp, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(p.Tmp, asset.Name)
	stage := p.App + ".new"
	cleanup := func() {
		os.RemoveAll(stage)
		os.RemoveAll(p.Tmp)
		os.MkdirAll(p.Tmp, 0o755)
	}

	rep.Phase(fmt.Sprintf("Downloading Brave Beta %s…", version))
	sum, err := download(asset.URL, zipPath, rep)
	if err != nil {
		cleanup()
		return err
	}

	rep.Phase("Verifying download…")
	rep.Progress(80, "")
	if want := expectedSum(rel, asset); want != "" {
		if !strings.EqualFold(want, sum) {
			cleanup()
			return fmt.Errorf("checksum mismatch: want %s got %s", want, sum)
		}
		log.Println("sha256 verified")
	} else {
		log.Printf("no checksum published, sha256=%s", sum)
	}
	if rep.Cancelled() {
		cleanup()
		return errCancelled
	}

	rep.Phase("Installing files…")
	rep.Progress(82, "")
	os.RemoveAll(stage)
	if err := extractZip(zipPath, stage, rep); err != nil {
		cleanup()
		return err
	}
	if _, err := os.Stat(filepath.Join(stage, "brave.exe")); err != nil {
		cleanup()
		return errors.New("extraction finished but brave.exe is missing")
	}

	rep.Phase("Finishing up…")
	rep.Progress(98, "")
	old := p.App + ".old"
	os.RemoveAll(old)
	if _, err := os.Stat(p.App); err == nil {
		if err := os.Rename(p.App, old); err != nil {
			cleanup()
			return fmt.Errorf("could not replace app folder, close Brave first: %w", err)
		}
	}
	if err := os.Rename(stage, p.App); err != nil {
		return err
	}
	os.RemoveAll(old)
	cleanup()
	if err := os.WriteFile(stateFile(p), []byte(rel.TagName), 0o644); err != nil {
		return err
	}
	rep.Progress(100, "")
	log.Printf("installed %s, Brave %s", rel.TagName, installedVersion(p))
	return nil
}

type dlWriter struct {
	rep         Reporter
	total, done int64
	start, last time.Time
}

func (w *dlWriter) Write(b []byte) (int, error) {
	w.done += int64(len(b))
	if w.rep.Cancelled() {
		return 0, errCancelled
	}
	now := time.Now()
	if now.Sub(w.last) < 200*time.Millisecond {
		return len(b), nil
	}
	w.last = now
	speed := float64(w.done) / now.Sub(w.start).Seconds()
	pct := 2
	detail := fmtBytes(w.done)
	if w.total > 0 {
		pct = 2 + int(w.done*78/w.total)
		left := ""
		if speed > 0 {
			left = "  •  " + fmtDur(float64(w.total-w.done)/speed) + " left"
		}
		detail = fmt.Sprintf("%s of %s  •  %s/s%s", fmtBytes(w.done), fmtBytes(w.total), fmtBytes(int64(speed)), left)
	}
	w.rep.Progress(pct, detail)
	return len(b), nil
}

func download(url, dest string, rep Reporter) (string, error) {
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
	now := time.Now()
	dl := &dlWriter{rep: rep, total: resp.ContentLength, start: now, last: now}
	if _, err := io.Copy(io.MultiWriter(f, h, dl), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type exWriter struct {
	rep         Reporter
	total, done int64
	last        time.Time
}

func (w *exWriter) Write(b []byte) (int, error) {
	w.done += int64(len(b))
	if w.rep.Cancelled() {
		return 0, errCancelled
	}
	now := time.Now()
	if now.Sub(w.last) < 200*time.Millisecond {
		return len(b), nil
	}
	w.last = now
	pct := 82
	if w.total > 0 {
		pct = 82 + int(w.done*16/w.total)
	}
	w.rep.Progress(pct, fmt.Sprintf("%s of %s", fmtBytes(w.done), fmtBytes(w.total)))
	return len(b), nil
}

func extractZip(zipPath, stage string, rep Reporter) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	var total int64
	for _, f := range r.File {
		total += int64(f.UncompressedSize64)
	}
	ew := &exWriter{rep: rep, total: total, last: time.Now()}
	cleanStage := filepath.Clean(stage) + string(os.PathSeparator)
	for _, f := range r.File {
		target := filepath.Join(stage, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(target, cleanStage) {
			return fmt.Errorf("unsafe path in archive: %q", f.Name)
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
		if err := writeZipEntry(f, target, ew); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, target string, ew *exWriter) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, io.TeeReader(rc, ew))
	return err
}
