package sensor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
)

func TestDecodePreservesSuppliedSession(t *testing.T) {
	for _, host := range adapters.Supported() {
		for _, id := range []string{"session-one", "雪/second-session", " opaque session "} {
			for _, mode := range []projection.Mode{projection.Full, projection.SHA256, projection.Omit} {
				raw, err := adapters.CanonicalHookEvent(host)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				payload["session_id"] = id
				encoded, _ := json.Marshal(payload)
				event, err := Decode(host, encoded, mode)
				if err != nil || event.SessionID != id || event.Host.Name != string(host) || event.Observation.Fidelity != schema.FidelityDirect {
					t.Fatalf("host=%s mode=%s event=%+v error=%v", host, mode, event, err)
				}
			}
		}
	}
}

func TestDecodeSessionAbsenceAndHostSpecificAliases(t *testing.T) {
	for _, test := range []struct {
		name   string
		host   adapters.Host
		fields map[string]any
		want   string
		fault  bool
	}{
		{"missing", adapters.Claude, nil, "", false},
		{"empty", adapters.Claude, map[string]any{"session_id": ""}, "", false},
		{"null", adapters.Claude, map[string]any{"session_id": nil}, "", false},
		{"wrong-session-type", adapters.Claude, map[string]any{"session_id": []string{"private-session"}}, "", true},
		{"cursor-conversation", adapters.Cursor, map[string]any{"conversation_id": "conversation", "generation_id": "turn-one"}, "conversation", false},
		{"cursor-next-generation", adapters.Cursor, map[string]any{"conversation_id": "conversation", "generation_id": "turn-two"}, "conversation", false},
		{"cursor-same-aliases", adapters.Cursor, map[string]any{"conversation_id": "conversation", "session_id": "conversation"}, "conversation", false},
		{"cursor-conflict", adapters.Cursor, map[string]any{"conversation_id": "private-conversation", "session_id": "private-session"}, "", true},
		{"cursor-null-conversation", adapters.Cursor, map[string]any{"conversation_id": nil, "session_id": "session"}, "session", false},
		{"cursor-empty-conversation", adapters.Cursor, map[string]any{"conversation_id": "", "session_id": "session"}, "session", false},
		{"cursor-wrong-type", adapters.Cursor, map[string]any{"conversation_id": map[string]string{"id": "private-session"}}, "", true},
		{"other-host-ignores-conversation-type", adapters.Claude, map[string]any{"conversation_id": []int{1}, "session_id": "session"}, "session", false},
		{"no-generation-fallback", adapters.Cursor, map[string]any{"generation_id": "turn-only", "tool_call_id": "tool-only", "request_id": "request-only"}, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, _ := adapters.CanonicalHookEvent(test.host)
			var payload map[string]any
			_ = json.Unmarshal(raw, &payload)
			for key, value := range test.fields {
				payload[key] = value
			}
			encoded, _ := json.Marshal(payload)
			event, err := Decode(test.host, encoded, projection.Omit)
			if test.fault {
				fault, ok := AsFault(err, "act_test")
				if !ok || fault.Reason != schema.ReasonMalformed || strings.Contains(err.Error(), "private-") {
					t.Fatalf("expected content-safe malformed fault, got %v", err)
				}
				return
			}
			if err != nil || event.SessionID != test.want {
				t.Fatalf("event=%+v error=%v", event, err)
			}
			if test.want == "" {
				wire, _ := json.Marshal(event)
				if strings.Contains(string(wire), "session_id") {
					t.Fatalf("unavailable session must be omitted: %s", wire)
				}
			}
		})
	}
}
