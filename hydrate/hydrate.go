package hydrate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	// PinPath is the repository-relative pin file: one trimmed semver line.
	PinPath = ".pitot/version"
	// DefaultHost is the distribution front door. Overridable via EnvHost for
	// tests and air-gapped mirrors; the registry behind it never appears here.
	DefaultHost = "get.operatorstack.systems"
	// EnvHost overrides the front-door host (scheme-carrying values allowed,
	// e.g. http://127.0.0.1:PORT for hermetic tests).
	EnvHost = "PITOT_GET_HOST"
	// EnvNoHydrate is the kill switch: when set to a non-empty value other
	// than "0", hydration is cache-only and misses fail closed.
	EnvNoHydrate = "PITOT_NO_HYDRATE"
	// EnvCacheDir overrides the cache root (hermetic tests).
	EnvCacheDir = "PITOT_CACHE_DIR"
)

var semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Pin reads the repository's committed version pin.
func Pin(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(PinPath)))
	if err != nil {
		return "", fmt.Errorf("pitot: read version pin %s: %w", PinPath, err)
	}
	pin := strings.TrimSpace(string(raw))
	if !semverPattern.MatchString(pin) {
		return "", fmt.Errorf("pitot: version pin %s must be one semver line, got %q", PinPath, pin)
	}
	return pin, nil
}

// WritePin records a version pin. The pin is the only version authority; a
// pin rewrite is the whole repository footprint of an upgrade.
func WritePin(root, version string) error {
	if !semverPattern.MatchString(version) {
		return fmt.Errorf("pitot: refusing to pin non-semver version %q", version)
	}
	path := filepath.Join(root, filepath.FromSlash(PinPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pitot: create %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, []byte(version+"\n"), 0o644)
}

// Host returns the front-door host, honoring the EnvHost override.
func Host() string {
	if host := os.Getenv(EnvHost); host != "" {
		return host
	}
	return DefaultHost
}

// BaseURL normalizes the front-door host into a scheme-carrying base URL.
func BaseURL() string {
	host := Host()
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimSuffix(host, "/")
	}
	return "https://" + host
}

// CacheRoot is the platform cache directory holding version slots. The rule
// must stay byte-identical with the shim scripts: POSIX (including darwin)
// uses ${XDG_CACHE_HOME:-$HOME/.cache}/pitot; Windows uses %LOCALAPPDATA%\pitot.
func CacheRoot() (string, error) {
	if dir := os.Getenv(EnvCacheDir); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "pitot"), nil
		}
		return "", errors.New("pitot: LOCALAPPDATA is not set")
	}
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "pitot"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("pitot: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".cache", "pitot"), nil
}

// Slot returns the write-once binary path for a version on this platform.
func Slot(version string) (string, error) {
	root, err := CacheRoot()
	if err != nil {
		return "", err
	}
	name := "pitot"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, version, runtime.GOOS+"-"+runtime.GOARCH, name), nil
}

// hydrateDisabled reports the kill switch state.
func hydrateDisabled() bool {
	value := os.Getenv(EnvNoHydrate)
	return value != "" && value != "0"
}

// Ensure materializes the pinned version's binary in its cache slot and
// returns the slot path. A populated slot is returned as-is (write-once); a
// miss downloads the release archive and checksums from the front door,
// verifies, extracts, and renames into place under a clone-wide lock.
func Ensure(ctx context.Context, version string) (string, error) {
	if !semverPattern.MatchString(version) {
		return "", fmt.Errorf("pitot: refusing to hydrate non-semver version %q", version)
	}
	slot, err := Slot(version)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(slot); err == nil {
		return slot, nil
	}
	if hydrateDisabled() {
		return "", fmt.Errorf("pitot: version %s is pinned by %s but its slot %s is absent and hydration is disabled (%s=1); unset the kill switch or hydrate the cache out of band (absence-fails-closed-and-named)", version, PinPath, slot, EnvNoHydrate)
	}

	slotDir := filepath.Dir(slot)
	if err := os.MkdirAll(slotDir, 0o755); err != nil {
		return "", fmt.Errorf("pitot: create slot directory: %w", err)
	}
	release, err := acquireLock(ctx, slotDir)
	if err != nil {
		return "", err
	}
	defer release()
	// Another process may have filled the slot while we waited on the lock.
	if _, err := os.Stat(slot); err == nil {
		return slot, nil
	}

	archiveName := fmt.Sprintf("pitot_%s_%s_%s.%s", version, runtime.GOOS, runtime.GOARCH, archiveExt())
	archive, err := fetch(ctx, fmt.Sprintf("%s/pitot/dl/%s/%s", BaseURL(), version, archiveName))
	if err != nil {
		return "", fmt.Errorf("pitot: version %s is pinned by %s but its slot %s is absent and the download failed: %w (absence-fails-closed-and-named)", version, PinPath, slot, err)
	}
	manifest, err := fetch(ctx, fmt.Sprintf("%s/pitot/dl/%s/checksums.txt", BaseURL(), version))
	if err != nil {
		return "", fmt.Errorf("pitot: download checksums.txt for %s: %w", version, err)
	}
	want, err := checksumFor(string(manifest), archiveName)
	if err != nil {
		return "", err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("pitot: checksum mismatch for %s: manifest says %s, archive is %s — refusing to install (nothing-executes-unverified)", archiveName, want, hex.EncodeToString(got[:]))
	}

	binary, err := extractBinary(archive)
	if err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(slotDir, ".hydrating-*")
	if err != nil {
		return "", fmt.Errorf("pitot: stage binary: %w", err)
	}
	tempPath := temp.Name()
	if _, err := temp.Write(binary); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return "", fmt.Errorf("pitot: stage binary: %w", err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempPath)
		return "", fmt.Errorf("pitot: stage binary: %w", err)
	}
	if err := os.Chmod(tempPath, 0o755); err != nil {
		os.Remove(tempPath)
		return "", fmt.Errorf("pitot: stage binary: %w", err)
	}
	if err := os.Rename(tempPath, slot); err != nil {
		os.Remove(tempPath)
		return "", fmt.Errorf("pitot: publish slot: %w", err)
	}
	return slot, nil
}

// Latest resolves the newest published version from the front door. Only
// `pitot upgrade` may call this — exec-time paths take the pin and nothing
// else (pin-is-the-only-version-authority).
func Latest(ctx context.Context) (string, error) {
	raw, err := fetch(ctx, BaseURL()+"/pitot/latest")
	if err != nil {
		return "", fmt.Errorf("pitot: resolve latest release: %w", err)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("pitot: parse latest release: %w", err)
	}
	version := strings.TrimPrefix(strings.TrimSpace(payload.Version), "v")
	if !semverPattern.MatchString(version) {
		return "", fmt.Errorf("pitot: latest release reported non-semver version %q", payload.Version)
	}
	return version, nil
}

// acquireLock serializes hydration of one slot across processes via an atomic
// mkdir; waiters poll until the holder finishes or the deadline passes.
func acquireLock(ctx context.Context, slotDir string) (func(), error) {
	lock := filepath.Join(slotDir, ".hydrate-lock")
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			return func() { os.Remove(lock) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("pitot: acquire hydration lock: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("pitot: hydration lock %s is held past the deadline; remove it if the holder crashed", lock)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func archiveExt() string {
	if runtime.GOOS == "windows" {
		return "zip"
	}
	return "tar.gz"
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, 256<<20))
}

// checksumFor extracts the sha256 for name from a "checksums.txt" manifest of
// "<hex>  <name>" lines.
func checksumFor(manifest, name string) (string, error) {
	for _, line := range strings.Split(manifest, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("pitot: checksums.txt has no entry for %s", name)
}

// extractBinary pulls the single pitot binary out of a release archive.
func extractBinary(archive []byte) ([]byte, error) {
	want := "pitot"
	if runtime.GOOS == "windows" {
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("pitot: open release zip: %w", err)
		}
		for _, file := range reader.File {
			if filepath.Base(file.Name) != want+".exe" {
				continue
			}
			open, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer open.Close()
			return io.ReadAll(io.LimitReader(open, 256<<20))
		}
		return nil, fmt.Errorf("pitot: release zip carries no %s.exe", want)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("pitot: open release archive: %w", err)
	}
	defer gz.Close()
	entries := tar.NewReader(gz)
	for {
		header, err := entries.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("pitot: release archive carries no %s binary", want)
		}
		if err != nil {
			return nil, fmt.Errorf("pitot: read release archive: %w", err)
		}
		if header.Typeflag == tar.TypeReg && filepath.Base(header.Name) == want {
			return io.ReadAll(io.LimitReader(entries, 256<<20))
		}
	}
}
