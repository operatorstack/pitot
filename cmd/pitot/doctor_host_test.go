package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const kimiHookConfig = `default_model = "pitot-control"
[[hooks]]
event = "PreToolUse"
matcher = "Bash"
command = "pitot hook kimi"
timeout = 5
`

func TestDoctorHostKimiReportsConfiguredHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(kimiHookConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	// Return value may be non-nil (the kimi binary is absent in CI); we assert on
	// the reported diagnostics, which must recognize the configured hook.
	_ = doctor([]string{"--host", "kimi"}, &stdout, &stderr)
	out := stdout.String()
	for _, want := range []string{
		"host check: kimi",
		filepath.Join(home, "config.toml"),
		"PreToolUse hook: FOUND",
		"fail-open",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor --host kimi output missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorHostKimiReportsMissingHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("default_model = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := doctor([]string{"--host", "kimi"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected doctor to report an issue when the hook is missing")
	}
	if !strings.Contains(stdout.String(), "PreToolUse hook: MISSING") {
		t.Errorf("expected MISSING hook diagnostic:\n%s", stdout.String())
	}
}

func TestDoctorHostRejectsUnknownHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := doctor([]string{"--host", "notahost"}, &stdout, &stderr); err == nil {
		t.Fatal("expected unsupported host to be rejected")
	}
}
