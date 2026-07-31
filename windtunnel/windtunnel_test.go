//go:build windtunnel

// Package windtunnel holds the integrated HEAD-to-HEAD check: it drives a host
// hook event all the way through the sensor and then routes a synchronous
// control request through the bridge, proving the observation and control planes
// agree at workspace HEAD. It is guarded by the `windtunnel` build tag so it runs
// as a dedicated target, not on every `go test ./...`.
//
// Today the check exercises Pitot's own planes against the canonical host events
// Boatstack emits (labs/12-.../hooks.go: canonicalHookEvent). When Boatstack
// starts importing Pitot, this file gains the cross-module leg (Boatstack
// producing the event, Pitot consuming it) without changing its shape.
package windtunnel

import (
	"testing"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/bridge"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
	"github.com/operatorstack/pitot/sensor"
)

// boatstackCanonicalEvents mirrors Boatstack's canonicalHookEvent output byte for
// byte. If Boatstack changes its host boundary, this fixture must change with it
// — that is exactly the drift the wind-tunnel exists to catch.
var boatstackCanonicalEvents = map[adapters.Host]string{
	adapters.Cursor:   `{"hook_event_name":"beforeShellExecution","command":"git status --short"}`,
	adapters.Claude:   `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`,
	adapters.Codex:    `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`,
	adapters.Gemini:   `{"hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"git status --short"}}`,
	adapters.Kimi:     `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`,
	adapters.Opencode: `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`,
	adapters.Copilot:  `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`,
	adapters.Qwen:     `{"hook_event_name":"PreToolUse","tool_name":"run_shell_command","tool_input":{"command":"git status --short"}}`,
	adapters.Pi:       `{"hook_event_name":"tool_call","tool_name":"bash","tool_input":{"command":"git status --short"}}`,
}

func TestSensorConsumesBoatstackCanonicalEvents(t *testing.T) {
	for host, raw := range boatstackCanonicalEvents {
		// The bytes Pitot's own adapter would emit must match what Boatstack emits.
		probe, err := adapters.CanonicalHookEvent(host)
		if err != nil {
			t.Fatalf("canonical event for %s: %v", host, err)
		}
		if string(probe) != raw {
			t.Fatalf("host %s canonical event drifted from Boatstack:\n pitot: %s\n boat:  %s", host, probe, raw)
		}
		event, err := sensor.Decode(host, []byte(raw), projection.SHA256)
		if err != nil {
			t.Fatalf("sensor could not consume %s event: %v", host, err)
		}
		if event.Action == nil || event.Action.Kind != "shell" {
			t.Fatalf("host %s: unexpected action %+v", host, event.Action)
		}
	}
}

func TestSensorConsumesPitotOnlyTransportBoundaries(t *testing.T) {
	var pitotOnly []adapters.Host
	for _, host := range adapters.Supported() {
		if _, sharedWithBoatstack := boatstackCanonicalEvents[host]; sharedWithBoatstack {
			continue
		}
		pitotOnly = append(pitotOnly, host)
		probe, err := adapters.CanonicalBoundaryEvent(host)
		if err != nil {
			t.Fatalf("canonical boundary for %s: %v", host, err)
		}
		event, err := sensor.Decode(host, probe, projection.SHA256)
		if err != nil {
			t.Fatalf("sensor could not consume %s boundary: %v", host, err)
		}
		if event.Action == nil || event.Action.Kind != "shell" || event.Observation.Source != schema.SourceHostEvent {
			t.Fatalf("host %s: unexpected boundary event %+v", host, event)
		}
	}
	if len(pitotOnly) != 1 || pitotOnly[0] != adapters.Devin {
		t.Fatalf("unexpected Pitot-only transport inventory: %v", pitotOnly)
	}
}

func TestControlRoundTripThroughBridge(t *testing.T) {
	router := bridge.NewRouter()
	if err := router.Register(bridge.Registration{
		Kind:          "release.approval",
		ControllerID:  "local-approval",
		DeadlineMS:    2000,
		OnTimeout:     schema.OutcomeDeny,
		OnUnavailable: schema.OutcomeDeny,
	}); err != nil {
		t.Fatal(err)
	}
	req := schema.ControlRequested{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlRequested,
		Kind:         "release.approval",
		ActionID:     "act_wind",
	}
	candidate := &schema.ControlResponse{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlResponse,
		ControllerID: "local-approval",
		ActionID:     "act_wind",
		Outcome:      schema.OutcomeAllow,
	}
	resp, err := router.Resolve(req, candidate)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resp.Outcome != schema.OutcomeAllow || resp.ActionID != "act_wind" {
		t.Fatalf("unexpected resolution %+v", resp)
	}
}
