package e2e

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/sensor"
)

// TestE2ESensorsConformityAcrossAllAdapters drives both kinds of Pitot sensors
// (Full and SHA256 projection modes) through a loop of all supported host adapters.
// It verifies that feeding the same semantic tool-execution inputs to any adapter
// returns the exact same normalized output schemas and actions.
func TestE2ESensorsConformityAcrossAllAdapters(t *testing.T) {
	cmdToVerify := "git status --short"

	h := sha256.New()
	h.Write([]byte(cmdToVerify))
	expectedHash := fmt.Sprintf("%x", h.Sum(nil))

	rawHostPayloads := map[adapters.Host]string{
		adapters.Claude:   `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`,
		adapters.Cursor:   `{"hook_event_name":"beforeShellExecution","command":"git status --short"}`,
		adapters.Codex:    `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`,
		adapters.Gemini:   `{"hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"git status --short"}}`,
		adapters.Kimi:     `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`,
		adapters.Opencode: `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`,
	}

	hosts := adapters.Supported()
	for _, host := range hosts {
		payload := rawHostPayloads[host]

		for _, mode := range []projection.Mode{projection.Full, projection.SHA256} {
			testName := fmt.Sprintf("%s/%s", host, mode)
			t.Run(testName, func(t *testing.T) {
				event, err := sensor.Decode(host, []byte(payload), mode)
				if err != nil {
					t.Fatalf("sensor decoding failed: %v", err)
				}

				if event.Type != "action.requested" {
					t.Errorf("expected type 'action.requested', got %q", event.Type)
				}
				if event.Action == nil || event.Action.Kind != "shell" {
					t.Errorf("expected action.kind 'shell', got %+v", event.Action)
				}
				if event.Content == nil {
					t.Fatal("expected non-nil content envelope")
				}
				if event.Content.Mode != string(mode) {
					t.Errorf("expected content mode %q, got %q", mode, event.Content.Mode)
				}

				switch mode {
				case projection.Full:
					fullContent := string(event.Content.Full)
					if fullContent == "" {
						t.Error("expected Full content to be populated")
					}
					if event.Content.SHA256 != "" {
						t.Errorf("expected SHA256 to be omitted, got %q", event.Content.SHA256)
					}
				case projection.SHA256:
					if len(event.Content.Full) > 0 {
						t.Errorf("expected Full content to be omitted, got %q", string(event.Content.Full))
					}
					if event.Content.SHA256 != expectedHash {
						t.Errorf("expected SHA256 hash %q, got %q", expectedHash, event.Content.SHA256)
					}
				}
			})
		}
	}
}
