package bridge

import (
	"fmt"
	"testing"

	"github.com/operatorstack/pitot/schema"
)

// The duplicate-detection window is bounded: long-lived runtimes stay at
// constant memory, and the window semantics (most recent resolvedWindow
// actions) are explicit.
func TestResolvedWindowIsBounded(t *testing.T) {
	router := NewRouter()
	if err := router.Register(Registration{Kind: "shell", ControllerID: "c1", DeadlineMS: 1000, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeDeny}); err != nil {
		t.Fatal(err)
	}
	request := func(i int) schema.ControlRequested {
		return schema.ControlRequested{
			PitotVersion: schema.Version,
			Type:         schema.TypeControlRequested,
			Kind:         "shell",
			ActionID:     fmt.Sprintf("act_%032d", i),
		}
	}
	for i := 0; i < resolvedWindow+10; i++ {
		if _, err := router.Resolve(request(i), nil); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if len(router.resolved) != resolvedWindow {
		t.Fatalf("resolved map should be capped at %d, got %d", resolvedWindow, len(router.resolved))
	}
	// A duplicate inside the window is still rejected.
	if _, err := router.Resolve(request(resolvedWindow+9), nil); err != ErrDuplicate {
		t.Fatalf("recent duplicate must be rejected, got %v", err)
	}
	// The oldest entries were evicted (documented window boundary).
	if _, dup := router.resolved[request(0).ActionID]; dup {
		t.Fatal("oldest action should have been evicted from the window")
	}
}
