package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsBoundary(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"doctor"}, &stdout, &stderr); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"local boundary", "cursor", "claude", "codex", "gemini", "decoder=PASS", "unauthenticated local socket: none"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q\n%s", want, out)
		}
	}
}

func TestRunRequiresConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"run"}, &stdout, &stderr); err == nil {
		t.Fatal("expected run without --config to fail")
	}
}

func TestRunValidatesConfigPath(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, ".pitot.yaml")
	if err := os.WriteFile(config, []byte("consumers: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"run", "--config", config}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "configuration boundary") {
		t.Errorf("unexpected output: %s", stdout.String())
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"fly"}, &stdout, &stderr); err == nil {
		t.Fatal("expected unknown command to fail")
	}
}

func TestHookCommandSubprocessBehavior(t *testing.T) {
	// 1. Test successful hook execution (allow)
	t.Run("allow", func(t *testing.T) {
		rawPayload := `{"hook_event_name":"beforeShellExecution","command":"git status"}`
		
		// Backup os.Stdin and restore later
		oldStdin := os.Stdin
		defer func() { os.Stdin = oldStdin }()
		
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdin = r
		
		// Write simulated payload on stdin and close
		go func() {
			_, _ = w.Write([]byte(rawPayload))
			_ = w.Close()
		}()
		
		var stdout, stderr bytes.Buffer
		if err := run([]string{"hook", "cursor"}, &stdout, &stderr); err != nil {
			t.Fatalf("hook allow failed: %v", err)
		}
		
		out := stdout.String()
		if !strings.Contains(out, `"type":"action.requested"`) {
			t.Errorf("stdout missing normalized event envelope:\n%s", out)
		}
	})

	// 2. Test blocked hook execution (deny)
	t.Run("deny", func(t *testing.T) {
		// Malformed Claude hook call missing command tool-input
		rawPayload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{}}`
		
		oldStdin := os.Stdin
		defer func() { os.Stdin = oldStdin }()
		
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdin = r
		
		go func() {
			_, _ = w.Write([]byte(rawPayload))
			_ = w.Close()
		}()
		
		var stdout, stderr bytes.Buffer
		err = run([]string{"hook", "claude"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected malformed hook to return error")
		}
		if err.Error() != "pitot: block" {
			t.Errorf("expected error 'pitot: block', got %q", err.Error())
		}
		
		errOut := stderr.String()
		if !strings.Contains(errOut, `"reason":"empty-command"`) {
			t.Errorf("stderr missing content-safe boundary fault:\n%s", errOut)
		}
	})
}
