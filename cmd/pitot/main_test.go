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

func TestRunRequiresConfigOrFragments(t *testing.T) {
	t.Setenv("PITOT_RUNTIME", "")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"run"}, &stdout, &stderr); err == nil {
		t.Fatal("expected run without a runtime path to fail")
	}
	// With a runtime path but neither --config nor fragments, run must point
	// the user at .pitot/conf.d.
	t.Chdir(t.TempDir())
	err := run([]string{"run", "--runtime", "runtime.json"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), ".pitot/conf.d") {
		t.Fatalf("expected no-fragments guidance, got %v", err)
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

// TestRunDiscoversFragmentsAndHonorsDir proves the tenant model end to end:
// `pitot run` with no --config discovers the .pitot/conf.d fragments by
// walking up from a SUBDIRECTORY to the owning root
// (discovery-walks-up-to-owned-roots-only), a controller from one tenant
// resolves explicit requests, and a consumer declared with dir: runs in its
// root-anchored working directory — its relative receipt path lands inside
// the tenant's own directory regardless of where pitot was invoked.
func TestRunDiscoversFragmentsAndHonorsDir(t *testing.T) {
	t.Setenv("PITOT_RUNTIME", "")
	helper := buildTestRole(t) // build before chdir: it compiles from the package dir
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	root := t.TempDir()
	t.Chdir(root)

	controllerFragment := fmt.Sprintf(`controllers:
  release.approval:
    id: release-policy
    command: [%q, "--role", "controller", "--id", "release-policy", "--nonce", "cli"]
    deadline_ms: 2000
    on_timeout: deny
    on_unavailable: deny
`, helper)
	consumerFragment := fmt.Sprintf(`consumers:
  - id: audit
    command: [%q, "--role", "consumer", "--receipt", "receipt.jsonl"]
    dir: "tenant-b"
    events: ["action.requested"]
    projection: {content: omit}
`, helper)
	confDir := filepath.Join(".pitot", "conf.d")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "a.yaml"), []byte(controllerFragment), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "b.yaml"), []byte(consumerFragment), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("tenant-b", 0o755); err != nil {
		t.Fatal(err)
	}
	// Invoke from a subdirectory: discovery must walk up to the owning root.
	if err := os.MkdirAll("apps/web", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root, "apps", "web"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runtimeOut bytes.Buffer
	var runtimeErr lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- runWithIO(ctx, []string{"run", "--runtime", runtimePath}, strings.NewReader(""), &runtimeOut, &runtimeErr)
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

	// The controller tenant answers explicit requests through the merged config.
	var requestOut bytes.Buffer
	if err := runWithIO(context.Background(), []string{"request", "release.approval", "--data", `{"phase":"ship"}`, "--runtime", runtimePath}, strings.NewReader(""), &requestOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("request through merged config: %v\n%s", err, requestOut.String())
	}
	if !strings.Contains(requestOut.String(), `"outcome":"allow"`) {
		t.Fatalf("request outcome: %s", requestOut.String())
	}

	// A hook observation fans to the consumer tenant, whose relative receipt
	// path must resolve inside its declared dir.
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status"}}`
	var hookOut, hookErr bytes.Buffer
	if err := runWithIO(context.Background(), []string{"hook", "claude", "--runtime", runtimePath}, strings.NewReader(payload), &hookOut, &hookErr); err != nil {
		t.Fatalf("hook err=%v stderr=%s", err, hookErr.String())
	}
	receipt := filepath.Join(root, "tenant-b", "receipt.jsonl")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(receipt); err == nil && strings.Contains(string(data), `"type":"action.requested"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer receipt never appeared in tenant dir %s\n%s", receipt, runtimeErr.String())
		}
		time.Sleep(20 * time.Millisecond)
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
