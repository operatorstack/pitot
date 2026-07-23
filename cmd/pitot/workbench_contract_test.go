package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatorstack/pitot/config"
)

// shellPolicyExpect maps each language to the source file init writes and the
// SDK controller-entry token that source must reference for the shell-policy
// template. The PITOT_DENY_ME marker is asserted separately for every language.
var shellPolicyExpect = map[string]struct {
	sourceFile string
	sdkToken   string
}{
	"python":     {"main.py", "run_controller"},
	"typescript": {"main.ts", "runController"},
	"go":         {"main.go", "sdk.RunController"},
	"rust":       {"main.rs", "run_controller"},
}

// TestInitShellPolicyContract asserts the shell-policy scaffold is coherent per
// language: expected files exist, the config parses and registers the controller
// under the shell kind, and the source references the SDK controller API plus
// the deny canary.
func TestInitShellPolicyContract(t *testing.T) {
	for lang, wantFiles := range initExpectations {
		lang, wantFiles := lang, wantFiles
		t.Run(lang, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "proj")
			var stdout, stderr bytes.Buffer
			if err := runInit([]string{"--language", lang, "--template", "shell-policy", "--dir", dir}, strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatalf("init shell-policy %s: %v", lang, err)
			}

			for _, name := range wantFiles {
				if _, statErr := readIf(dir, name); statErr != nil {
					t.Errorf("%s: expected generated file %q: %v", lang, name, statErr)
				}
			}

			// The generated config must parse and register the controller for shell.
			loaded, err := config.Load(filepath.Join(dir, ".pitot.yaml"))
			if err != nil {
				t.Fatalf("%s: generated .pitot.yaml did not parse: %v", lang, err)
			}
			ctrl, ok := loaded.Config.Controllers["shell"]
			if !ok {
				t.Fatalf("%s: controller not registered under shell kind: %+v", lang, loaded.Config.Controllers)
			}
			if ctrl.ID != "local-shell-policy" {
				t.Errorf("%s: controller id = %q, want local-shell-policy", lang, ctrl.ID)
			}
			if len(ctrl.Command) == 0 {
				t.Errorf("%s: controller command is empty", lang)
			}

			// The source must use the SDK controller API and the deny canary.
			want := shellPolicyExpect[lang]
			src, err := readIf(dir, want.sourceFile)
			if err != nil {
				t.Fatalf("%s: read source: %v", lang, err)
			}
			if !strings.Contains(src, want.sdkToken) {
				t.Errorf("%s: source missing SDK controller API %q:\n%s", lang, want.sdkToken, src)
			}
			if !strings.Contains(src, "PITOT_DENY_ME") {
				t.Errorf("%s: source missing PITOT_DENY_ME canary:\n%s", lang, src)
			}
			if !strings.Contains(src, "local-shell-policy") {
				t.Errorf("%s: source missing local-shell-policy id:\n%s", lang, src)
			}
		})
	}
}

// TestInitNextStepLaunchesAgentNotController guards the corrected guidance: the
// hint must point users at `pitot dev --host HOST -- AGENT`, never at `--exec`
// with the Controller command.
func TestInitNextStepLaunchesAgentNotController(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	var stdout, stderr bytes.Buffer
	if err := runInit([]string{"--language", "go", "--template", "shell-policy", "--dir", dir}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("init: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "pitot dev --host HOST -- AGENT") {
		t.Errorf("next-step guidance does not launch an agent:\n%s", out)
	}
	if strings.Contains(out, "--exec") {
		t.Errorf("next-step guidance still uses --exec (points at the Controller):\n%s", out)
	}
	if strings.Contains(out, "go run main.go") {
		t.Errorf("next-step guidance still passes the Controller command as the agent:\n%s", out)
	}
}

// TestInitBlankControllerUsesApprovalKind confirms non-shell controller
// templates keep the test.approval kind (and do not leak the shell canary).
func TestInitBlankControllerUsesApprovalKind(t *testing.T) {
	for _, template := range []string{"blank-controller", "release-approval"} {
		template := template
		t.Run(template, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "proj")
			var stdout, stderr bytes.Buffer
			if err := runInit([]string{"--language", "go", "--template", template, "--dir", dir}, strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatalf("init %s: %v", template, err)
			}
			loaded, err := config.Load(filepath.Join(dir, ".pitot.yaml"))
			if err != nil {
				t.Fatalf("%s: config did not parse: %v", template, err)
			}
			if _, ok := loaded.Config.Controllers["test.approval"]; !ok {
				t.Errorf("%s: expected test.approval controller, got %+v", template, loaded.Config.Controllers)
			}
			if _, ok := loaded.Config.Controllers["shell"]; ok {
				t.Errorf("%s: must not register a shell controller", template)
			}
		})
	}
}

// TestInitTemplateRoleConsistency verifies template/role validation.
func TestInitTemplateRoleConsistency(t *testing.T) {
	t.Run("mismatch rejected", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "proj")
		var stdout, stderr bytes.Buffer
		err := runInit([]string{"--language", "go", "--role", "consumer", "--template", "shell-policy", "--dir", dir}, strings.NewReader(""), &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "implies role") {
			t.Fatalf("expected role/template mismatch error, got %v", err)
		}
	})
	t.Run("unsupported template rejected", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "proj")
		var stdout, stderr bytes.Buffer
		err := runInit([]string{"--language", "go", "--template", "nonesuch", "--dir", dir}, strings.NewReader(""), &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "unsupported template") {
			t.Fatalf("expected unsupported template error, got %v", err)
		}
	})
	t.Run("blank-consumer infers consumer role", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "proj")
		var stdout, stderr bytes.Buffer
		if err := runInit([]string{"--language", "go", "--template", "blank-consumer", "--dir", dir}, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatalf("init blank-consumer: %v", err)
		}
		loaded, err := config.Load(filepath.Join(dir, ".pitot.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Config.Consumers) == 0 {
			t.Errorf("blank-consumer did not produce a consumer config: %+v", loaded.Config)
		}
	})
}

// readIf returns a file's contents under dir, or the read error.
func readIf(dir, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	return string(data), err
}
