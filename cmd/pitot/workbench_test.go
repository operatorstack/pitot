package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initExpectations maps each language to the files a fresh controller project
// must contain to be runnable without further setup.
var initExpectations = map[string][]string{
	"python":     {"main.py", "requirements.txt", "pyproject.toml", ".pitot.yaml"},
	"typescript": {"main.ts", "package.json", "tsconfig.json", ".pitot.yaml"},
	"go":         {"main.go", "go.mod", ".pitot.yaml"},
	"rust":       {"main.rs", "Cargo.toml", ".pitot.yaml"},
}

func TestInitGeneratesRunnableProjectPerLanguage(t *testing.T) {
	for lang, wantFiles := range initExpectations {
		lang, wantFiles := lang, wantFiles
		t.Run(lang, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "proj")
			var stdout, stderr bytes.Buffer
			err := runInit([]string{"--language", lang, "--role", "controller", "--dir", dir}, strings.NewReader(""), &stdout, &stderr)
			if err != nil {
				t.Fatalf("init %s: %v", lang, err)
			}
			for _, name := range wantFiles {
				if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
					t.Errorf("%s: expected generated file %q: %v", lang, name, statErr)
				}
			}
			cfg, readErr := os.ReadFile(filepath.Join(dir, ".pitot.yaml"))
			if readErr != nil {
				t.Fatalf("read .pitot.yaml: %v", readErr)
			}
			if !strings.Contains(string(cfg), "controllers:") {
				t.Errorf("%s: controller config missing controllers block:\n%s", lang, cfg)
			}
		})
	}
}

func TestInitConsumerRoleWritesConsumerConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	var stdout, stderr bytes.Buffer
	if err := runInit([]string{"--language", "go", "--role", "consumer", "--dir", dir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, ".pitot.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "consumers:") {
		t.Errorf("consumer config missing consumers block:\n%s", cfg)
	}
	src, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "RunConsumer") {
		t.Errorf("consumer source is not a consumer:\n%s", src)
	}
}

func TestInitRejectsInvalidRole(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	var stdout, stderr bytes.Buffer
	err := runInit([]string{"--language", "python", "--role", "admin", "--dir", dir}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected invalid role to be rejected")
	}
	if !strings.Contains(err.Error(), "unsupported role") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestInitRejectsInvalidLanguage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	var stdout, stderr bytes.Buffer
	err := runInit([]string{"--language", "cobol", "--dir", dir}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unsupported language") {
		t.Fatalf("expected unsupported language error, got %v", err)
	}
}

func TestInitIsNonDestructive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	var out, errb bytes.Buffer
	if err := runInit([]string{"--language", "go", "--role", "controller", "--dir", dir}, strings.NewReader(""), &out, &errb); err != nil {
		t.Fatalf("first init: %v", err)
	}
	// Second init without --force must refuse.
	err := runInit([]string{"--language", "go", "--role", "controller", "--dir", dir}, strings.NewReader(""), &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected non-destructive refusal, got %v", err)
	}
	// With --force it must succeed.
	if err := runInit([]string{"--language", "go", "--role", "controller", "--dir", dir, "--force"}, strings.NewReader(""), &out, &errb); err != nil {
		t.Fatalf("forced init: %v", err)
	}
}

func TestInitRequiresLanguageWhenNonInteractiveAndUndetectable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "empty")
	var stdout, stderr bytes.Buffer
	err := runInit([]string{"--dir", dir}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--language is required") {
		t.Fatalf("expected language-required error, got %v", err)
	}
}

func TestInitDetectsLanguageFromExistingManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	// Non-interactive with no --language: detection must select rust. --force
	// because Cargo.toml already exists.
	if err := runInit([]string{"--dir", dir, "--role", "controller", "--force"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("init with detection: %v", err)
	}
	if !strings.Contains(stdout.String(), "Detected rust") {
		t.Errorf("expected rust detection, got:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "main.rs")); err != nil {
		t.Errorf("expected rust source generated: %v", err)
	}
}

func TestDevRejectsUnsupportedHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runDev(context.Background(), []string{"--host", "notahost", "--exec", "true"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unsupported host") {
		t.Fatalf("expected unsupported host error, got %v", err)
	}
}

func TestDevRequiresHostAndExec(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runDev(context.Background(), []string{"--host", "claude"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error when --exec missing")
	}
	if err := runDev(context.Background(), []string{"--exec", "true"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error when --host missing")
	}
}
