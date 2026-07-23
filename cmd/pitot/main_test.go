package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(value)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestDoctorReportsBoundary(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"doctor"}, &stdout, &stderr); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"local boundary", "claude", "codex", "copilot", "cursor", "gemini", "kimi", "opencode", "pi", "qwen", "decoder=PASS", "unauthenticated local socket: none", "hook_control consumer_delivery explicit_request"} {
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

func buildTestRole(t *testing.T) string {
	t.Helper()
	name := "pitot-testrole"
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-o", path, "../../internal/testrole")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build test role: %v\n%s", err, output)
	}
	return path
}

func TestRuntimeBacksHookAndExplicitRequestCommands(t *testing.T) {
	t.Setenv("PITOT_RUNTIME", "")
	dir := t.TempDir()
	config := filepath.Join(dir, ".pitot.yaml")
	runtimePath := filepath.Join(dir, "runtime.json")
	helper := buildTestRole(t)
	raw := fmt.Sprintf(`consumers:
  - id: audit
    command: [%q, "--role", "consumer", "--receipt", %q]
    events: ["action.requested"]
    projection: {content: omit}
controllers:
  shell:
    id: shell-policy
    command: [%q, "--role", "controller", "--id", "shell-policy", "--nonce", "cli"]
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
  release.approval:
    id: release-policy
    command: [%q, "--role", "controller", "--id", "release-policy", "--nonce", "cli"]
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
`, helper, filepath.Join(dir, "consumer.jsonl"), helper, helper)
	if err := os.WriteFile(config, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runtimeOut bytes.Buffer
	var runtimeErr lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- runWithIO(ctx, []string{"run", "--config", config, "--runtime", runtimePath}, strings.NewReader(""), &runtimeOut, &runtimeErr)
	}()
	for i := 0; i < 250; i++ {
		if _, err := os.Stat(runtimePath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("runtime did not become ready: %v\n%s", err, runtimeErr.String())
	}
	var requestOut bytes.Buffer
	err := runWithIO(context.Background(), []string{"request", "release.approval", "--data", `{"phase":"PITOT_DENY"}`, "--runtime", runtimePath}, strings.NewReader(""), &requestOut, &bytes.Buffer{})
	if !errors.Is(err, errBlocked) || !strings.Contains(requestOut.String(), `"outcome":"deny"`) {
		t.Fatalf("request err=%v output=%s", err, requestOut.String())
	}
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"PITOT_DENY cli"}}`
	var hookOut, hookErr bytes.Buffer
	err = runWithIO(context.Background(), []string{"hook", "claude", "--runtime", runtimePath}, strings.NewReader(payload), &hookOut, &hookErr)
	if !errors.Is(err, errBlocked) || !strings.Contains(hookErr.String(), "PITOT_CONTROLLER_DENY cli") || !strings.Contains(hookOut.String(), `"type":"action.requested"`) {
		t.Fatalf("hook err=%v stdout=%s stderr=%s", err, hookOut.String(), hookErr.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"fly"}, &stdout, &stderr); err == nil {
		t.Fatal("expected unknown command to fail")
	}
}

func TestHookCommandSubprocessBehavior(t *testing.T) {
	t.Setenv("PITOT_RUNTIME", "")
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

	// Kimi Code uses the blocking PreToolUse/Bash payload shape.
	t.Run("kimi allow", func(t *testing.T) {
		rawPayload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status"}}`

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
		if err := run([]string{"hook", "kimi"}, &stdout, &stderr); err != nil {
			t.Fatalf("Kimi hook allow failed: %v", err)
		}
		if !strings.Contains(stdout.String(), `"host":{"name":"kimi"`) ||
			!strings.Contains(stdout.String(), `"kind":"shell"`) {
			t.Errorf("stdout missing normalized Kimi shell event:\n%s", stdout.String())
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
