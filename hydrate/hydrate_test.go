package hydrate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// buildArchive wraps a stub binary in the release archive shape goreleaser
// produces for this platform (tar.gz on POSIX, zip on Windows, binary nested
// in the archive directory).
func buildArchive(t *testing.T, version string, binary []byte) (name string, payload []byte) {
	t.Helper()
	name = fmt.Sprintf("pitot_%s_%s_%s.%s", version, runtime.GOOS, runtime.GOARCH, archiveExt())
	inner := fmt.Sprintf("pitot_%s_%s_%s/pitot", version, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		inner += ".exe"
		var buf bytes.Buffer
		writer := zip.NewWriter(&buf)
		entry, err := writer.Create(inner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(binary); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return name, buf.Bytes()
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	archive := tar.NewWriter(gz)
	if err := archive.WriteHeader(&tar.Header{Name: inner, Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return name, buf.Bytes()
}

// artifactServer is a hermetic stand-in for the distribution front door,
// serving /pitot/latest and /pitot/dl/<version>/<file> from memory.
type artifactServer struct {
	base      string
	downloads atomic.Int64
	close     func()
}

func newArtifactServer(t *testing.T, latest string, files map[string][]byte) *artifactServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &artifactServer{base: "http://" + listener.Addr().String()}
	mux := http.NewServeMux()
	mux.HandleFunc("/pitot/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":%q}`, latest)
	})
	mux.HandleFunc("/pitot/dl/", func(w http.ResponseWriter, r *http.Request) {
		payload, ok := files[strings.TrimPrefix(r.URL.Path, "/pitot/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		server.downloads.Add(1)
		w.Write(payload)
	})
	httpServer := &http.Server{Handler: mux}
	go httpServer.Serve(listener)
	server.close = func() { httpServer.Close() }
	t.Cleanup(server.close)
	return server
}

func checksumLine(name string, payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]) + "  " + name + "\n"
}

func setupRelease(t *testing.T, version string, binary []byte) *artifactServer {
	t.Helper()
	name, payload := buildArchive(t, version, binary)
	server := newArtifactServer(t, version, map[string][]byte{
		version + "/" + name:            payload,
		version + "/" + "checksums.txt": []byte(checksumLine(name, payload)),
	})
	t.Setenv(EnvHost, server.base)
	t.Setenv(EnvCacheDir, t.TempDir())
	t.Setenv(EnvNoHydrate, "")
	return server
}

// control-law: slots-are-write-once
//
// A hydrated slot is never rewritten: the second Ensure is a pure cache hit
// that works with the front door completely gone.
func TestEnsureHydratesOnceThenSlotIsImmutable(t *testing.T) {
	binary := []byte("#!/bin/sh\necho stub-1.2.3\n")
	server := setupRelease(t, "1.2.3", binary)

	first, err := Ensure(context.Background(), "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(first)
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("slot content mismatch (err=%v)", err)
	}
	afterFirst := server.downloads.Load()
	if afterFirst == 0 {
		t.Fatal("first Ensure did not download")
	}

	server.close() // front door gone: cache must carry everything
	second, err := Ensure(context.Background(), "1.2.3")
	if err != nil {
		t.Fatalf("cache hit failed with the server down: %v", err)
	}
	if second != first {
		t.Fatalf("slot path changed: %s vs %s", first, second)
	}
	if server.downloads.Load() != afterFirst {
		t.Fatal("second Ensure downloaded again")
	}
}

// control-law: nothing-executes-unverified
//
// A tampered archive is refused by name and leaves the slot absent.
func TestEnsureRefusesTamperedArchive(t *testing.T) {
	name, payload := buildArchive(t, "1.2.3", []byte("evil"))
	server := newArtifactServer(t, "1.2.3", map[string][]byte{
		"1.2.3/" + name:            payload,
		"1.2.3/" + "checksums.txt": []byte(strings.Repeat("0", 64) + "  " + name + "\n"),
	})
	t.Setenv(EnvHost, server.base)
	t.Setenv(EnvCacheDir, t.TempDir())
	t.Setenv(EnvNoHydrate, "")

	_, err := Ensure(context.Background(), "1.2.3")
	if err == nil || !strings.Contains(err.Error(), "nothing-executes-unverified") {
		t.Fatalf("want named checksum refusal, got %v", err)
	}
	slot, err := Slot("1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(slot); !os.IsNotExist(statErr) {
		t.Fatalf("tampered hydration must leave no slot, stat err=%v", statErr)
	}
	entries, _ := os.ReadDir(filepath.Dir(slot))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".hydrating-") {
			t.Fatalf("staging temp file leaked: %s", entry.Name())
		}
	}
}

// control-law: absence-fails-closed-and-named
//
// The kill switch and a dead front door both fail closed with errors naming
// the pin, the slot, and (for the kill switch) the escape hatch. There is no
// PATH fallback to fail open into.
func TestEnsureAbsenceFailsClosedAndNamed(t *testing.T) {
	t.Setenv(EnvCacheDir, t.TempDir())

	t.Run("kill switch", func(t *testing.T) {
		t.Setenv(EnvNoHydrate, "1")
		_, err := Ensure(context.Background(), "1.2.3")
		if err == nil {
			t.Fatal("want refusal")
		}
		for _, want := range []string{PinPath, EnvNoHydrate, "absence-fails-closed-and-named"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal must name %q, got: %v", want, err)
			}
		}
	})

	t.Run("front door unreachable", func(t *testing.T) {
		t.Setenv(EnvNoHydrate, "")
		t.Setenv(EnvHost, "http://127.0.0.1:1")
		_, err := Ensure(context.Background(), "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "absence-fails-closed-and-named") {
			t.Fatalf("want named fail-closed error, got %v", err)
		}
	})
}

// control-law: pin-is-the-only-version-authority
//
// A newer version sitting in the cache is invisible to a pinned Ensure, and
// the pin file itself only accepts one semver line.
func TestPinIsTheOnlyVersionAuthority(t *testing.T) {
	binary := []byte("pinned")
	setupRelease(t, "1.0.0", binary)

	// Seed a "newer" slot by hand: it must not shadow the pinned version.
	newer, err := Slot("9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(newer), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("newer"), 0o755); err != nil {
		t.Fatal(err)
	}

	path, err := Ensure(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("pinned Ensure resolved the wrong binary (err=%v, got %q)", err, got)
	}

	root := t.TempDir()
	if err := WritePin(root, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	pin, err := Pin(root)
	if err != nil || pin != "1.0.0" {
		t.Fatalf("pin round-trip: %q %v", pin, err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(PinPath)), []byte("latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Pin(root); err == nil {
		t.Fatal("non-semver pin must be rejected — 'latest' is not a version authority")
	}
}

func TestChecksumForAndLatest(t *testing.T) {
	manifest := "abc  other.tar.gz\n" + checksumLine("pitot_1.0.0_linux_amd64.tar.gz", []byte("x"))
	digest := sha256.Sum256([]byte("x"))
	got, err := checksumFor(manifest, "pitot_1.0.0_linux_amd64.tar.gz")
	if err != nil || got != hex.EncodeToString(digest[:]) {
		t.Fatalf("checksumFor: %q %v", got, err)
	}
	if _, err := checksumFor(manifest, "missing"); err == nil {
		t.Fatal("missing manifest entry must error")
	}

	server := newArtifactServer(t, "v2.0.0", nil)
	t.Setenv(EnvHost, server.base)
	latest, err := Latest(context.Background())
	if err != nil || latest != "2.0.0" {
		t.Fatalf("Latest: %q %v", latest, err)
	}
}
