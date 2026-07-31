package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Observation-only stays exit-0 but is never silent (Locus root-cause:
// silent-mode-degradation).
func TestHookWithoutRuntimeAnnouncesObserveOnly(t *testing.T) {
	t.Setenv("PITOT_RUNTIME", "")
	stdin := strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status"}}`)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if err := runWithIO(context.Background(), []string{"hook", "claude"}, stdin, stdout, stderr); err != nil {
		t.Fatalf("observe-only hook must exit clean: %v", err)
	}
	if !strings.Contains(stdout.String(), `"action.requested"`) {
		t.Fatalf("normalized event missing: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "observe-only") {
		t.Fatalf("observe-only notice missing: %q", stderr.String())
	}
}
