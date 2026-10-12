package deepseekacp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeepSeekACPFixture(t *testing.T) {
	mode := os.Getenv("PITOT_DEEPSEEK_ACP_FIXTURE")
	if mode == "" {
		return
	}
	var patchPath string
	for i, arg := range os.Args {
		if arg == "--patch" && i+1 < len(os.Args) {
			patchPath = os.Args[i+1]
		}
	}
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		os.Exit(2)
	}
	var nonce string
	for _, line := range strings.Split(string(patch), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "nonce:") {
			_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "nonce:"))), &nonce)
		}
	}
	if nonce == "" || !strings.Contains(string(patch), "pitotShellControl") {
		os.Exit(3)
	}
	if mode == "wrong-ready" {
		nonce = "stale-ready"
	}
	_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"event": "pitot-shell-control-ready", "nonce": nonce})
	if mode == "wrong-ready" {
		os.Exit(0)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(4)
		}
		var result any
		switch message.Method {
		case "initialize":
			result = map[string]int{"protocolVersion": 1}
		case "session/new":
			result = map[string]string{"sessionId": "owned-session"}
		case "session/prompt":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "owned-session", "update": map[string]any{"sessionUpdate": "tool_call", "content": []any{}}}})
			if mode == "wait" {
				child := exec.Command(os.Args[0], "-test.run=^TestDeepSeekDescendantFixture$")
				child.Stdout = os.Stdout
				child.Stderr = os.Stderr
				if child.Start() != nil {
					os.Exit(6)
				}
				time.Sleep(30 * time.Second)
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "owned-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "completed"}}}})
			result = map[string]string{"stopReason": "end_turn"}
		case "session/close":
			result = map[string]any{}
		default:
			os.Exit(5)
		}
		id := message.ID
		if mode == "wrong-id" {
			id++
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	os.Exit(0)
}

func TestDeepSeekDescendantFixture(t *testing.T) {
	path := os.Getenv("PITOT_DEEPSEEK_DESCENDANT_MARKER")
	if path == "" {
		return
	}
	_ = os.WriteFile(path+".started", []byte("started"), 0o600)
	time.Sleep(2 * time.Second)
	_ = os.WriteFile(path, []byte("orphan executed"), 0o600)
	os.Exit(0)
}

func TestDeepSeekVersionFixture(t *testing.T) {
	if os.Getenv("PITOT_DEEPSEEK_VERSION_FIXTURE") == "" {
		return
	}
	if os.Getenv("NODE_OPTIONS") != "" || os.Getenv("NODE_PATH") != "" || !strings.HasPrefix(os.Getenv("TMP"), os.Getenv("TMPDIR")) || !strings.HasPrefix(os.Getenv("DSH_HOME"), os.Getenv("HOME")) {
		os.Exit(3)
	}
	_, _ = io.WriteString(os.Stdout, "0.2.0-rc.2\n")
	os.Exit(0)
}

func TestVersionRemovesAmbientNodeBootstrap(t *testing.T) {
	t.Setenv("PITOT_DEEPSEEK_VERSION_FIXTURE", "1")
	t.Setenv("NODE_OPTIONS", "--require /unrelated")
	t.Setenv("NODE_PATH", "/unrelated")
	version, err := Version(context.Background(), os.Args[0], []string{"-test.run=^TestDeepSeekVersionFixture$", "--"})
	if err != nil || version != "0.2.0-rc.2" {
		t.Fatalf("version=%q err=%v", version, err)
	}
}

func TestOwnedDeepSeekACP(t *testing.T) {
	for _, mode := range []string{"success", "wrong-ready", "wrong-id", "wait"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PITOT_DEEPSEEK_ACP_FIXTURE", mode)
			marker := filepath.Join(t.TempDir(), "orphan-marker")
			t.Setenv("PITOT_DEEPSEEK_DESCENDANT_MARKER", marker)
			root := filepath.Join(t.TempDir(), "owned session")
			var output strings.Builder
			ctx := context.Background()
			if mode == "wait" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			err := Run(ctx, Options{Executable: os.Args[0], Arguments: []string{"-test.run=^TestDeepSeekACPFixture$", "--"}, Cwd: t.TempDir(), Prompt: "test prompt", Runtime: filepath.Join(t.TempDir(), "runtime.json"), Pitot: os.Args[0], Stdout: &output, Stderr: io.Discard, SessionDir: root})
			if mode == "success" {
				if err != nil || output.String() != "completed" {
					t.Fatalf("output=%q err=%v", output.String(), err)
				}
			} else if err == nil {
				t.Fatal("invalid ACP exchange succeeded")
			}
			if mode == "wait" {
				if _, err := os.Stat(marker + ".started"); err != nil {
					t.Fatalf("descendant was never observed running: %v", err)
				}
				time.Sleep(1500 * time.Millisecond)
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("DeepSeek descendant survived: %v", err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "control.patch.yml")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestDeepSeekOutputFailure(t *testing.T) {
	t.Setenv("PITOT_DEEPSEEK_ACP_FIXTURE", "success")
	err := Run(context.Background(), Options{Executable: os.Args[0], Arguments: []string{"-test.run=^TestDeepSeekACPFixture$", "--"}, Cwd: t.TempDir(), Prompt: "test prompt", Runtime: filepath.Join(t.TempDir(), "runtime.json"), Pitot: os.Args[0], Stdout: failingOutput{}, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("lost output was accepted: %v", err)
	}
}
