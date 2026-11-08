package geodata

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fearless743/fboard-node/internal/nlog"
)

const (
	xrayGeoIPURL   = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat"
	xrayGeoSiteURL = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat"
)

var httpClient = &http.Client{Timeout: 10 * time.Minute}

func Ensure(dir string, needGeoIP, needGeoSite bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create geo_data_dir %q: %w", dir, err)
	}

	type entry struct {
		name string
		url  string
	}

	var files []entry
	if needGeoIP {
		files = append(files, entry{"geoip.dat", xrayGeoIPURL})
	}
	if needGeoSite {
		files = append(files, entry{"geosite.dat", xrayGeoSiteURL})
	}

	var firstErr error
	for _, f := range files {
		if err := ensureFile(dir, f.name, f.url); err != nil {
			nlog.Core().Warn("geo database auto-download failed", "file", f.name, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func ensureFile(dir, name, url string) error {
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	nlog.Core().Info("geo database missing, downloading automatically", "file", name, "url", url)
	if err := atomicDownload(dst, url); err != nil {
		return err
	}
	// Best-effort integrity check: when the upstream publishes a "<file>.sha256"
	// sidecar, verify the download against it. Absence only warns — it must
	// not block startup (mirrors warn-and-continue policy elsewhere).
	if err := verifySidecarSHA256(dst, url); err != nil {
		return err
	}
	if fi, err := os.Stat(dst); err == nil {
		nlog.Core().Info("geo database ready", "file", name, "size_kb", fi.Size()/1024)
	}
	return nil
}

func atomicDownload(dst, url string) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("http get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status from %s: %s", url, resp.Status)
	}

	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp)

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("write geo database: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("rename to destination: %w", err)
	}
	return nil
}

// verifySidecarSHA256 fetches "<url>.sha256" and compares it against the
// sha256 of the downloaded file at dst. A missing/unreachable sidecar only
// logs a warning and does not fail. A present-but-mismatching sidecar deletes
// the file and returns an error.
func verifySidecarSHA256(dst, url string) error {
	resp, err := httpClient.Get(url + ".sha256")
	if err != nil {
		nlog.Core().Warn("geo sha256 sidecar unreachable, skipping verification", "file", dst, "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		nlog.Core().Warn("geo sha256 sidecar missing, skipping verification", "file", dst, "status", resp.Status)
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		nlog.Core().Warn("geo sha256 sidecar unreadable, skipping verification", "file", dst, "error", err)
		return nil
	}
	// Sidecars are either bare hex or "<hex>  <filename>" (sha256sum format).
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		nlog.Core().Warn("geo sha256 sidecar empty, skipping verification", "file", dst)
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(fields[0]))
	if len(want) != 64 {
		nlog.Core().Warn("geo sha256 sidecar malformed, skipping verification", "file", dst)
		return nil
	}
	f, err := os.Open(dst)
	if err != nil {
		return fmt.Errorf("open downloaded file: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash downloaded file: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(dst)
		return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", dst, want, got)
	}
	nlog.Core().Info("geo database checksum verified", "file", dst)
	return nil
}
