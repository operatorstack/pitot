package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/operatorstack/pitot/hydrate"
)

// stubRelease serves a fake front door whose release binary is a POSIX shell
// script (so hydrated "binaries" are runnable in tests without a compiler).
func stubRelease(t *testing.T, version, script string) (base string, shutdown func()) {
	t.Helper()
	archiveName := fmt.Sprintf("pitot_%s_%s_%s.tar.gz", version, goruntime.GOOS, goruntime.GOARCH)
	inner := fmt.Sprintf("pitot_%s_%s_%s/pitot", version, goruntime.GOOS, goruntime.GOARCH)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	archive := tar.NewWriter(gz)
	if err := archive.WriteHeader(&tar.Header{Name: inner, Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	payload := buf.Bytes()
	digest := sha256.Sum256(payload)
	files := map[string][]byte{
		version + "/" + archiveName: payload,
		version + "/checksums.txt":  []byte(hex.EncodeToString(digest[:]) + "  " + archiveName + "\n"),
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pitot/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":%q}`, version)
	})
	mux.HandleFunc("/pitot/dl/", func(w http.ResponseWriter, r *http.Request) {
		payload, ok := files[strings.TrimPrefix(r.URL.Path, "/pitot/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(payload)
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return "http://" + listener.Addr().String(), func() { server.Close() }
}

// stubBinaryScript is the hydrated stand-in binary: answers `version` with a
// parseable protocol line and echoes everything else.
func stubBinaryScript(protocol string) string {
	return fmt.Sprintf("#!/bin/sh\nif [ \"${1:-}\" = version ]; then\n  echo \"  protocol version : %s\"\n  exit 0\nfi\necho \"stub-run $*\"\n", protocol)
}

// control-law: the-shim-carries-no-policy
//
// The emitted shims read the pin, fetch, verify, and exec — they carry no
// reference to tenant config or any version channel, and the substrate is
// idempotent (ensured by whoever arrives first, never rewritten).
func TestShimCarriesNoPolicy(t *testing.T) {
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	written, err := writeSubstrate(&out, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 3 {
		t.Fatalf("expected pin + 2 shims written, got %v", written)
	}
	pin, err := hydrate.Pin(".")
	if err != nil || pin != "1.2.3" {
		t.Fatalf("pin: %q %v", pin, err)
	}
	for _, shim := range []string{".pitot/bin/pitot", ".pitot/bin/pitot.ps1"} {
		body, err := os.ReadFile(filepath.FromSlash(shim))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"conf.d", "/latest", "controllers", "consumers"} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s must carry no policy surface, found %q", shim, forbidden)
			}
		}
		for _, required := range []string{".pitot/version", "checksum", "nothing-executes-unverified", "absence-fails-closed-and-named"} {
			if !strings.Contains(string(body), required) {
				t.Errorf("%s must reference %q", shim, required)
			}
		}
	}
	// Idempotent: a second ensure writes nothing and rewrites nothing.
	before, _ := os.ReadFile(filepath.FromSlash(".pitot/bin/pitot"))
	again, err := writeSubstrate(&out, "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("substrate must be write-once per repo, rewrote %v", again)
	}
	if pin, _ := hydrate.Pin("."); pin != "1.2.3" {
		t.Fatalf("existing pin must never be overwritten, got %q", pin)
	}
	after, _ := os.ReadFile(filepath.FromSlash(".pitot/bin/pitot"))
	if !bytes.Equal(before, after) {
		t.Fatal("existing shim was rewritten")
	}

	// A dev build cannot vouch for a version: no pin, guidance instead.
	t.Chdir(t.TempDir())
	out.Reset()
	if _, err := writeSubstrate(&out, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := hydrate.Pin("."); err == nil {
		t.Fatal("dev build must not write a pin")
	}
	if !strings.Contains(out.String(), "unreleased pitot build") {
		t.Fatalf("dev build must print pin guidance, got: %s", out.String())
	}
}

// control-law: pin-is-the-only-version-authority
// control-law: absence-fails-closed-and-named
//
// The POSIX shim end to end: first invocation hydrates the pinned release
// from the front door (sha256-verified), later invocations are pure cache
// hits that survive the front door disappearing, and the kill switch with an
// empty cache fails closed by name.
func TestShimHydratesPinnedReleaseThenCacheHits(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the POSIX shim smoke needs sh, curl, and tar")
	}
	base, shutdown := stubRelease(t, "1.2.3", stubBinaryScript("1"))
	t.Chdir(t.TempDir())
	cache := t.TempDir()
	if _, err := writeSubstrate(&bytes.Buffer{}, "1.2.3"); err != nil {
		t.Fatal(err)
	}

	run := func() (string, error) {
		command := exec.Command("sh", filepath.FromSlash(".pitot/bin/pitot"), "hello", "world")
		command.Env = append(os.Environ(), "PITOT_GET_HOST="+base, "PITOT_CACHE_DIR="+cache, "PITOT_NO_HYDRATE=")
		output, err := command.CombinedOutput()
		return string(output), err
	}
	first, err := run()
	if err != nil || !strings.Contains(first, "stub-run hello world") {
		t.Fatalf("first shim run: %v\n%s", err, first)
	}
	shutdown() // front door gone: the cache must carry everything
	second, err := run()
	if err != nil || !strings.Contains(second, "stub-run hello world") {
		t.Fatalf("cache-hit shim run with the front door down: %v\n%s", err, second)
	}

	// Kill switch + empty cache: fail closed, by name, nonzero exit.
	command := exec.Command("sh", filepath.FromSlash(".pitot/bin/pitot"))
	command.Env = append(os.Environ(), "PITOT_GET_HOST="+base, "PITOT_CACHE_DIR="+t.TempDir(), "PITOT_NO_HYDRATE=1")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "absence-fails-closed-and-named") {
		t.Fatalf("kill switch must fail closed by name: err=%v\n%s", err, output)
	}
}

// snapshotTree hashes every file under root (relative path -> sha256).
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		rel, _ := filepath.Rel(root, path)
		tree[filepath.ToSlash(rel)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// control-law: upgrade-is-a-reviewed-data-diff
//
// The whole repository footprint of an upgrade is one rewritten pin line.
func TestUpgradeIsAReviewedDataDiff(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the hydrated stand-in binary is a POSIX shell script")
	}
	base, _ := stubRelease(t, "2.0.0", stubBinaryScript("1"))
	t.Chdir(t.TempDir())
	t.Setenv(hydrate.EnvHost, base)
	t.Setenv(hydrate.EnvCacheDir, t.TempDir())
	t.Setenv(hydrate.EnvNoHydrate, "")

	if _, err := writeSubstrate(&bytes.Buffer{}, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	writeFragment(t, ".", "tool-a", "requires_protocol: \"1\"\ncontrollers:\n  test.approval:\n    id: tool-a\n    command: [\"true\"]\n    deadline_ms: 1000\n    on_timeout: deny\n    on_unavailable: deny\n")

	before := snapshotTree(t, ".")
	var out bytes.Buffer
	if err := runUpgrade(context.Background(), []string{"--to", "2.0.0"}, &out, &out); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out.String())
	}
	after := snapshotTree(t, ".")

	var changed []string
	for path, digest := range after {
		if before[path] != digest {
			changed = append(changed, path)
		}
	}
	if len(changed) != 1 || changed[0] != ".pitot/version" {
		t.Fatalf("upgrade footprint must be exactly the pin, changed: %v", changed)
	}
	if pin, _ := hydrate.Pin("."); pin != "2.0.0" {
		t.Fatalf("pin after upgrade: %q", pin)
	}
	if !strings.Contains(out.String(), "commit this diff") {
		t.Fatalf("upgrade must direct the operator to commit the pin diff:\n%s", out.String())
	}
}

// control-law: upgrade-preserves-the-tenancy-contract
//
// A new binary that no longer speaks a tenant's protocol blocks the upgrade
// by fragment name, and the pin does not move.
func TestUpgradeRefusesTenantIncompatibleTarget(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the hydrated stand-in binary is a POSIX shell script")
	}
	base, _ := stubRelease(t, "2.0.0", stubBinaryScript("2"))
	t.Chdir(t.TempDir())
	t.Setenv(hydrate.EnvHost, base)
	t.Setenv(hydrate.EnvCacheDir, t.TempDir())
	t.Setenv(hydrate.EnvNoHydrate, "")

	if _, err := writeSubstrate(&bytes.Buffer{}, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	writeFragment(t, ".", "tool-a", "requires_protocol: \"1\"\ncontrollers:\n  test.approval:\n    id: tool-a\n    command: [\"true\"]\n    deadline_ms: 1000\n    on_timeout: deny\n    on_unavailable: deny\n")

	var out bytes.Buffer
	err := runUpgrade(context.Background(), []string{"--to", "2.0.0"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "upgrade-preserves-the-tenancy-contract") || !strings.Contains(err.Error(), "tool-a.yaml") {
		t.Fatalf("want tenant-named refusal, got %v", err)
	}
	if pin, _ := hydrate.Pin("."); pin != "1.0.0" {
		t.Fatalf("refused upgrade must not move the pin, got %q", pin)
	}
}

// control-law: bindings-move-in-lockstep-with-the-CLI
// control-law: packages-come-from-our-registry-or-nowhere
//
// Install writes only front-door registry config (scoped npm line, pip index
// file), pins bindings to the CLI's own version, and reverts cleanly.
func TestInstallWritesFrontDoorConfigWithExactPins(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(hydrate.EnvHost, "front.example.test")

	// A released CLI pins bindings to itself.
	previous := version
	version = "3.1.4"
	defer func() { version = previous }()

	var out bytes.Buffer
	if err := runInstall([]string{"typescript", "--configure-only"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	// npm honors the project .npmrc only inside a project: install must have
	// created a minimal private package.json in the empty directory.
	manifest, err := os.ReadFile("package.json")
	if err != nil {
		t.Fatalf("install must create package.json in a bare directory: %v", err)
	}
	if !strings.Contains(string(manifest), `"private": true`) {
		t.Fatalf("created package.json must be private:\n%s", manifest)
	}
	// An existing package.json is never touched.
	if err := os.WriteFile("package.json", []byte(`{"name":"mine"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInstall([]string{"typescript", "--configure-only"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile("package.json"); string(kept) != `{"name":"mine"}` {
		t.Fatalf("existing package.json was rewritten:\n%s", kept)
	}
	npmrc, err := os.ReadFile(".npmrc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(npmrc), "@operatorstack:registry=https://front.example.test/npm/") {
		t.Fatalf(".npmrc must point the scope at the front door:\n%s", npmrc)
	}
	if strings.Contains(string(npmrc), "pkg.dev") {
		t.Fatal("registry internals must never appear in client config")
	}
	if !strings.Contains(out.String(), "@operatorstack/pitot@3.1.4") {
		t.Fatalf("npm binding must be pinned to the CLI version:\n%s", out.String())
	}

	if err := runInstall([]string{"python", "--configure-only"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	registry, err := os.ReadFile(filepath.FromSlash(".pitot/registry"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(registry), "PIP_INDEX_URL=https://front.example.test/pip/simple/") {
		t.Fatalf(".pitot/registry must point pip at the front door:\n%s", registry)
	}
	if !strings.Contains(out.String(), "operatorstack-pitot==3.1.4") {
		t.Fatalf("python binding must be pinned to the CLI version:\n%s", out.String())
	}

	// Revert removes exactly what install owns.
	if err := runInstall([]string{"typescript", "--revert"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(".npmrc"); !os.IsNotExist(err) {
		t.Fatal("revert must remove the .npmrc it created")
	}
	if err := runInstall([]string{"python", "--revert"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.FromSlash(".pitot/registry")); !os.IsNotExist(err) {
		t.Fatal("revert must remove .pitot/registry")
	}
}
