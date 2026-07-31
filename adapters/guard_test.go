package adapters

import "testing"

// The decoder holds the same tool-scope line the host matcher promises, so a
// widened or drifted host config cannot promote another tool's input to a
// supervised shell action.
func TestCommandForRejectsNonShellTools(t *testing.T) {
	cases := map[Host]struct {
		accepted []string
	}{
		Claude:   {accepted: []string{"Bash"}},
		Codex:    {accepted: []string{"Bash"}},
		Kimi:     {accepted: []string{"Bash"}},
		Opencode: {accepted: []string{"Bash"}},
		Gemini:   {accepted: []string{"run_shell_command"}},
		Pi:       {accepted: []string{"bash"}},
		Qwen:     {accepted: []string{"Bash", "run_shell_command"}},
	}
	for host, tc := range cases {
		for _, tool := range tc.accepted {
			raw := RawBoundaryEvent{ToolName: tool, ToolInput: map[string]any{"command": "git status"}}
			if command, ok := host.CommandFor(raw); !ok || command != "git status" {
				t.Errorf("%s: expected tool %q accepted, got ok=%v", host, tool, ok)
			}
		}
		raw := RawBoundaryEvent{ToolName: "Write", ToolInput: map[string]any{"command": "rm -rf /"}}
		if _, ok := host.CommandFor(raw); ok {
			t.Errorf("%s: non-shell tool %q must not yield a supervised shell command", host, "Write")
		}
	}
}

func TestActionKindNeverSilentlyDefaults(t *testing.T) {
	if kind := Claude.ActionKind("SomeFutureEvent"); kind != "" {
		t.Fatalf("unknown boundary event must have no kind, got %q", kind)
	}
	// An omitted discriminator means the host's main boundary event.
	if kind := Claude.ActionKind(""); kind != "shell" {
		t.Fatalf("empty event name should resolve via the main boundary event, got %q", kind)
	}
	if kind := Cursor.ActionKind("beforeMCPExecution"); kind != "mcp" {
		t.Fatalf("registered kinds must be preserved, got %q", kind)
	}
	if kind := Host("unregistered").ActionKind("PreToolUse"); kind != "" {
		t.Fatalf("unregistered host must have no kind, got %q", kind)
	}
}
