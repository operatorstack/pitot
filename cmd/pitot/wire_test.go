package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func runInitHost(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runInit(append([]string{"--host"}, args...), strings.NewReader(""), &out, &out)
	return out.String(), err
}

// control-law: wiring-is-marker-owned-and-witnessed
// control-law: one-canonical-entry-per-event
//
// End to end on claude: foreign config survives wiring byte-meaningfully,
// exactly one canonical marked entry exists, the fragment witnesses it, and
// re-running is idempotent.
func TestWireHostClaudeEndToEnd(t *testing.T) {
	t.Chdir(t.TempDir())
	seed := `{"theme":"dark","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"./mine.sh"}]}]}}`
	if err := os.MkdirAll(".claude", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".claude/settings.json", []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runInitHost(t, "claude")
	if err != nil {
		t.Fatalf("wire claude: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(".claude/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["theme"] != "dark" {
		t.Fatal("foreign top-level key lost")
	}
	entries := settings["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(entries) != 2 {
		t.Fatalf("want foreign + canonical, got %d entries", len(entries))
	}
	if !strings.Contains(string(raw), "mine.sh") || !strings.Contains(string(raw), ".pitot/bin/pitot hook claude") {
		t.Fatalf("config missing foreign or canonical entry:\n%s", raw)
	}
	if _, err := os.Stat(filepath.FromSlash(".pitot/hooks/claude.fragment.json")); err != nil {
		t.Fatalf("fragment witness missing: %v", err)
	}

	// Idempotent: a second wire changes nothing.
	before, _ := os.ReadFile(".claude/settings.json")
	if _, err := runInitHost(t, "claude"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(".claude/settings.json")
	if !bytes.Equal(before, after) {
		t.Fatal("re-wiring rewrote an already-canonical config")
	}
}

// control-law: drift-is-named-not-silently-fixed
//
// A hand-edited marked entry: init refuses without force, doctor names the
// drift without mutating, doctor --fix restores the canonical entry while
// the foreign entry survives.
func TestWireHostDriftFlow(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := runInitHost(t, "claude"); err != nil {
		t.Fatal(err)
	}
	// Hand-edit the marked entry.
	raw, _ := os.ReadFile(".claude/settings.json")
	edited := bytes.Replace(raw, []byte(`"matcher": "Bash"`), []byte(`"matcher": "Bash|Edit"`), 1)
	if bytes.Equal(raw, edited) {
		t.Fatalf("test setup: matcher not found in\n%s", raw)
	}
	if err := os.WriteFile(".claude/settings.json", edited, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runInitHost(t, "claude"); err == nil || !strings.Contains(err.Error(), "drift-is-named-not-silently-fixed") {
		t.Fatalf("drifted entry must refuse an implicit rewrite, got %v", err)
	}

	var out bytes.Buffer
	doctorErr := doctor([]string{"--host", "claude"}, &out, &out)
	if doctorErr == nil || !strings.Contains(out.String(), "DRIFTED") {
		t.Fatalf("doctor must name the drift, err=%v out=%s", doctorErr, out.String())
	}
	afterDoctor, _ := os.ReadFile(".claude/settings.json")
	if !bytes.Equal(edited, afterDoctor) {
		t.Fatal("doctor mutated the config without --fix")
	}

	out.Reset()
	if err := doctor([]string{"--host", "claude", "--fix"}, &out, &out); err != nil {
		t.Fatalf("doctor --fix: %v\n%s", err, out.String())
	}
	fixed, _ := os.ReadFile(".claude/settings.json")
	if bytes.Contains(fixed, []byte("Bash|Edit")) {
		t.Fatal("--fix did not restore the canonical entry")
	}
	var okOut bytes.Buffer
	if err := doctor([]string{"--host", "claude"}, &okOut, &okOut); err != nil || !strings.Contains(okOut.String(), "FOUND") {
		t.Fatalf("post-fix doctor: err=%v out=%s", err, okOut.String())
	}
}

// control-law: wiring-points-at-the-repo-shim
//
// Cursor wiring emits the committed bridge (0755, defaulting to the sibling
// shim) and a version-1 hooks file whose entry invokes the bridge.
func TestWireHostCursorWritesBridge(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := runInitHost(t, "cursor"); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.FromSlash(".pitot/bin/hooks/cursor-beforeShellExecution")
	info, err := os.Stat(bridge)
	if err != nil {
		t.Fatalf("bridge missing: %v", err)
	}
	// POSIX execute bits do not exist on Windows; there Cursor runs the
	// commandWindows PowerShell path instead of the bridge.
	if goruntime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Fatal("bridge is not executable")
	}
	body, _ := os.ReadFile(bridge)
	if !strings.Contains(string(body), `PITOT_COMMAND="${PITOT_BIN:-$shim}"`) {
		t.Fatalf("bridge must default to the repo shim:\n%s", body)
	}
	raw, _ := os.ReadFile(filepath.FromSlash(".cursor/hooks.json"))
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["version"] != float64(1) {
		t.Fatalf("cursor hooks.json missing version 1:\n%s", raw)
	}
	if !strings.Contains(string(raw), ".pitot/bin/hooks/cursor-beforeShellExecution") {
		t.Fatalf("cursor entry does not invoke the bridge:\n%s", raw)
	}
}

// User-level hosts get the exact wiring snippet, never an edit; unknown
// hosts are rejected; --host excludes scaffold flags.
func TestWireHostGuidanceAndFlagRules(t *testing.T) {
	t.Chdir(t.TempDir())
	out, err := runInitHost(t, "kimi")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"config.toml", "[[hooks]]", "does not edit files outside the repository"} {
		if !strings.Contains(out, want) {
			t.Errorf("kimi guidance missing %q:\n%s", want, out)
		}
	}
	if entries, _ := os.ReadDir("."); len(entries) != 0 {
		t.Fatalf("guidance must write nothing, found %v", entries)
	}
	if _, err := runInitHost(t, "notahost"); err == nil || !strings.Contains(err.Error(), "unsupported host") {
		t.Fatalf("unknown host must be rejected, got %v", err)
	}
	var buf bytes.Buffer
	err = runInit([]string{"--host", "claude", "--language", "go"}, strings.NewReader(""), &buf, &buf)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("--host must exclude scaffold flags, got %v", err)
	}
	if err := doctor([]string{"--fix"}, &buf, &buf); err == nil || !strings.Contains(err.Error(), "--fix requires --host") {
		t.Fatalf("doctor --fix without --host must be rejected, got %v", err)
	}
}
