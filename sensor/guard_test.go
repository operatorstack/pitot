package sensor

import (
	"errors"
	"strings"
	"testing"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/schema"
)

// A non-shell tool carrying a command string must fault at the decoder even
// if the host matcher forwarded it (defense in depth for the five hosts that
// previously accepted any tool_input.command).
func TestDecodeRejectsNonShellToolInput(t *testing.T) {
	payloads := map[adapters.Host]string{
		adapters.Claude:   `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"command":"rm -rf /"}}`,
		adapters.Codex:    `{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"command":"curl evil"}}`,
		adapters.Gemini:   `{"hook_event_name":"BeforeTool","tool_name":"write_file","tool_input":{"command":"true"}}`,
		adapters.Opencode: `{"hook_event_name":"PreToolUse","tool_name":"webfetch","tool_input":{"command":"true"}}`,
		adapters.Kimi:     `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"command":"true"}}`,
		adapters.Pi:       `{"hook_event_name":"tool_call","tool_name":"editor","tool_input":{"command":"true"}}`,
	}
	for host, payload := range payloads {
		_, err := Decode(host, []byte(payload), "full")
		var fault *FaultError
		if !errors.As(err, &fault) || fault.Reason != schema.ReasonEmptyCommand {
			t.Errorf("%s: non-shell tool must fault with empty-command, got %v", host, err)
		}
	}
}

// Payloads that omit the event discriminator keep decoding as the host's main
// boundary event — the tolerated-empty-name behavior is a feature and must
// not regress into the removed silent shell fallback.
func TestDecodeEmptyEventNameResolvesMainBoundary(t *testing.T) {
	event, err := Decode(adapters.Claude, []byte(`{"tool_name":"Bash","tool_input":{"command":"git status"}}`), "full")
	if err != nil {
		t.Fatalf("empty event name should decode via the main boundary event: %v", err)
	}
	if event.Action.Kind != "shell" {
		t.Fatalf("expected shell kind, got %q", event.Action.Kind)
	}
	if !strings.Contains(string(event.Content.Full), "git status") {
		t.Fatalf("expected command content, got %s", event.Content.Full)
	}
}
