package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initExpectations maps each language to the project files a fresh controller
// scaffold must contain to be runnable without further setup. The runtime
// registration lands separately in the tenant fragment.
var initExpectations = map[string][]string{
	"python":     {"main.py", "requirements.txt", "pyproject.toml"},
	"typescript": {"main.ts", "package.json", "tsconfig.json"},
	"go":         {"main.go", "go.mod"},
	"rust":       {"main.rs", "Cargo.toml"},
}

func TestInitGeneratesRunnableProjectPerLanguage(t *testing.T) {
	for lang, wantFiles := range initExpectations {
		lang, wantFiles := lang, wantFiles
		t.Run(lang, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var stdout, stderr bytes.Buffer
			err := runInit([]string{"--language", lang, "--role", "controller", "--dir", "proj"}, strings.NewReader(""), &stdout, &stderr)
			if err != nil {
				t.Fatalf("init %s: %v", lang, err)
			}
			for _, name := range wantFiles {
				if _, statErr := os.Stat(filepath.Join("proj", name)); statErr != nil {
					t.Errorf("%s: expected generated file %q: %v", lang, name, statErr)
				}
			}
			cfg, readErr := os.ReadFile(filepath.Join(".pitot", "conf.d", "proj.yaml"))
			if readErr != nil {
				t.Fatalf("read tenant fragment: %v", readErr)
			}
			if !strings.Contains(string(cfg), "controllers:") {
				t.Errorf("%s: controller fragment missing controllers block:\n%s", lang, cfg)
			}
			if !strings.Contains(string(cfg), `dir: "proj"`) {
				t.Errorf("%s: fragment must pin the project working directory:\n%s", lang, cfg)
			}
		})
	}
}

func TestInitConsumerRoleWritesConsumerConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runInit([]string{"--language", "go", "--role", "consumer", "--dir", "proj"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(".pitot", "conf.d", "proj.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "consumers:") {
		t.Errorf("consumer fragment missing consumers block:\n%s", cfg)
	}
	src, err := os.ReadFile(filepath.Join("proj", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "RunConsumer") {
		t.Errorf("consumer source is not a consumer:\n%s", src)
	}
}

// Two tenants initialized into the same repository must never collide: each
// gets its own project directory and its own fragment, and the merged config
// discovers both.
func TestInitIsAdditiveAcrossTenants(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runInit([]string{"--language", "go", "--role", "controller", "--dir", "tool-a"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("first tenant: %v", err)
	}
	if err := runInit([]string{"--language", "go", "--role", "consumer", "--dir", "tool-b"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("second tenant: %v", err)
	}
	for _, fragment := range []string{"tool-a.yaml", "tool-b.yaml"} {
		if _, err := os.Stat(filepath.Join(".pitot", "conf.d", fragment)); err != nil {
			t.Errorf("expected fragment %s: %v", fragment, err)
		}
	}
}

// A second tenant claiming an already-owned request kind must fail at
// registration time — before any file is written — naming both fragments.
func TestInitPreflightsKindCollision(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runInit([]string{"--language", "go", "--template", "shell-policy", "--dir", "tool-a"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("first tenant: %v", err)
	}
	err := runInit([]string{"--language", "go", "--template", "shell-policy", "--dir", "tool-b"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `request kind "shell" is claimed by both .pitot/conf.d/tool-a.yaml and .pitot/conf.d/tool-b.yaml`) {
		t.Fatalf("expected kind collision naming both fragments, got %v", err)
	}
	if _, statErr := os.Stat("tool-b"); !os.IsNotExist(statErr) {
		t.Errorf("refused registration must not scaffold the project, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(".pitot", "conf.d", "tool-b.yaml")); !os.IsNotExist(statErr) {
		t.Errorf("refused registration must not write the fragment, stat err=%v", statErr)
	}
}

func TestInitRejectsInvalidRole(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	err := runInit([]string{"--language", "python", "--role", "admin", "--dir", "proj"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected invalid role to be rejected")
	}
	if !strings.Contains(err.Error(), "unsupported role") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestInitRejectsInvalidLanguage(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	err := runInit([]string{"--language", "cobol", "--dir", "proj"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unsupported language") {
		t.Fatalf("expected unsupported language error, got %v", err)
	}
}

func TestInitRejectsInvalidFragmentName(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	err := runInit([]string{"--language", "go", "--role", "controller", "--dir", "proj", "--fragment", "Bad Name"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "invalid fragment name") {
		t.Fatalf("expected fragment name rejection, got %v", err)
	}
}

func TestInitIsNonDestructive(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errb bytes.Buffer
	if err := runInit([]string{"--language", "go", "--role", "controller", "--dir", "proj"}, strings.NewReader(""), &out, &errb); err != nil {
		t.Fatalf("first init: %v", err)
	}
	// Second init without --force must refuse — the existing fragment belongs
	// to a tenant.
	err := runInit([]string{"--language", "go", "--role", "controller", "--dir", "proj"}, strings.NewReader(""), &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected non-destructive refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), ".pitot/conf.d/proj.yaml") {
		t.Fatalf("refusal must name the contested fragment, got %v", err)
	}
	// With --force it must succeed.
	if err := runInit([]string{"--language", "go", "--role", "controller", "--dir", "proj", "--force"}, strings.NewReader(""), &out, &errb); err != nil {
		t.Fatalf("forced init: %v", err)
	}
}

func TestInitRequiresLanguageWhenNonInteractiveAndUndetectable(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	err := runInit([]string{"--dir", "empty"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--language is required") {
		t.Fatalf("expected language-required error, got %v", err)
	}
}

func TestInitDetectsLanguageFromExistingManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("proj", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("proj", "Cargo.toml"), []byte("[package]\nname=\"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	// Non-interactive with no --language: detection must select rust. --force
	// because Cargo.toml already exists.
	if err := runInit([]string{"--dir", "proj", "--role", "controller", "--force"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("init with detection: %v", err)
	}
	if !strings.Contains(stdout.String(), "Detected rust") {
		t.Errorf("expected rust detection, got:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join("proj", "main.rs")); err != nil {
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
