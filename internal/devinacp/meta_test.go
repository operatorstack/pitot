package devinacp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A tool_call update missing the vendor _meta tool name leaves the command
// cache empty (fail-closed rejections follow). That degradation must be
// named once per session, not mistaken for Controller policy.
func TestMetaAbsenceIsDiagnosedOnce(t *testing.T) {
	stderr := &bytes.Buffer{}
	transport := &client{stderr: stderr, stdout: &bytes.Buffer{}, commands: map[string]string{}}
	update := json.RawMessage(`{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","rawInput":{"command":"git status"}}}`)
	for i := 0; i < 2; i++ {
		if err := transport.handleUpdate(update); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	if len(transport.commands) != 0 {
		t.Fatalf("no command may be cached without the vendor tool name, got %v", transport.commands)
	}
	if got := strings.Count(stderr.String(), "cognition.ai/inferenceToolName"); got != 1 {
		t.Fatalf("expected exactly one diagnostic, got %d in %q", got, stderr.String())
	}
}

// The normal path still caches and emits no diagnostic.
func TestMetaPresenceCachesWithoutDiagnostic(t *testing.T) {
	stderr := &bytes.Buffer{}
	transport := &client{stderr: stderr, stdout: &bytes.Buffer{}, commands: map[string]string{}}
	update := json.RawMessage(`{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","rawInput":{"command":"git status"},"_meta":{"cognition.ai/inferenceToolName":"exec"}}}`)
	if err := transport.handleUpdate(update); err != nil {
		t.Fatal(err)
	}
	if transport.commands[commandKey("s1", "t1")] != "git status" {
		t.Fatalf("command not cached: %v", transport.commands)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected diagnostic: %q", stderr.String())
	}
}
