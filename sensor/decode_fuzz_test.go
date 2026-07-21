package sensor

import (
	"testing"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/projection"
)

// FuzzDecode drives the decoder entrypoint with arbitrary bytes across every
// host. The contract under fuzz is total: Decode must always either return a
// well-formed event or a *FaultError with a content-safe reason — never panic
// and never leak the raw payload into the fault. The seed corpus mirrors the
// malformed host-event vectors (empty, invalid JSON, truncated).
func FuzzDecode(f *testing.F) {
	seeds := [][]byte{
		[]byte(``),
		[]byte(`{`),
		[]byte(`{not json`),
		[]byte(`{"hook_event_name":"beforeShellExecution","command":"git status --short"}`),
		[]byte(`{"hook_event_name":"beforeShellExecution","command":""}`),
		[]byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`),
		[]byte(`{"hook_event_name":"PreToolUse","tool_input":null}`),
		[]byte(`{"hook_event_name":123}`),
		[]byte(`[]`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	hosts := adapters.Supported()
	modes := []projection.Mode{projection.Full, projection.SHA256, projection.Omit}

	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, host := range hosts {
			for _, mode := range modes {
				event, err := Decode(host, raw, mode)
				if err != nil {
					fault, ok := AsFault(err, "act_fuzz")
					if !ok {
						t.Fatalf("non-fault error for host %s: %v", host, err)
					}
					if fault.Reason == "" {
						t.Fatalf("fault for host %s has empty reason", host)
					}
					continue
				}
				// A successful decode must be well-formed.
				if event.Action == nil {
					t.Fatalf("decoded event for host %s has no action", host)
				}
				if event.Content == nil || event.Content.Mode != string(mode) {
					t.Fatalf("decoded event for host %s has wrong content: %+v", host, event.Content)
				}
			}
		}
	})
}
