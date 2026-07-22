package sensor

import (
	"strings"
	"testing"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
)

func TestDecodeCanonicalEvents(t *testing.T) {
	for _, host := range adapters.Supported() {
		probe, err := adapters.CanonicalHookEvent(host)
		if err != nil {
			t.Fatalf("canonical event for %s: %v", host, err)
		}
		event, err := Decode(host, probe, projection.SHA256)
		if err != nil {
			t.Fatalf("decode %s canonical event: %v", host, err)
		}
		if event.PitotVersion != schema.Version {
			t.Errorf("%s: version = %q, want %q", host, event.PitotVersion, schema.Version)
		}
		if event.Type != schema.TypeActionRequested {
			t.Errorf("%s: type = %q, want %q", host, event.Type, schema.TypeActionRequested)
		}
		if event.Host.Name != string(host) {
			t.Errorf("%s: host name = %q", host, event.Host.Name)
		}
		if event.Observation.Source != schema.SourceHostHook || event.Observation.Fidelity != schema.FidelityDirect {
			t.Errorf("%s: observation = %+v", host, event.Observation)
		}
		if event.Content == nil || event.Content.Mode != schema.ContentSHA256 || event.Content.SHA256 == "" {
			t.Errorf("%s: content = %+v", host, event.Content)
		}
	}
}

func TestDecodeMalformedIsFault(t *testing.T) {
	_, err := Decode(adapters.Cursor, []byte("{not json"), projection.Omit)
	fault, ok := AsFault(err, "act_1")
	if !ok {
		t.Fatalf("expected fault error, got %v", err)
	}
	if fault.Reason != schema.ReasonMalformed {
		t.Errorf("reason = %q, want %q", fault.Reason, schema.ReasonMalformed)
	}
	if fault.Type != schema.TypeBoundaryFault || fault.Host != string(adapters.Cursor) {
		t.Errorf("fault = %+v", fault)
	}
}

func TestDecodeEmptyCommandIsFault(t *testing.T) {
	raw := []byte(`{"hook_event_name":"beforeShellExecution","command":""}`)
	_, err := Decode(adapters.Cursor, raw, projection.Omit)
	fault, ok := AsFault(err, "act_2")
	if !ok {
		t.Fatalf("expected fault error, got %v", err)
	}
	if fault.Reason != schema.ReasonEmptyCommand {
		t.Errorf("reason = %q, want %q", fault.Reason, schema.ReasonEmptyCommand)
	}
}

func TestDecodeUnsupportedHostIsFault(t *testing.T) {
	_, err := Decode(adapters.Host("aider"), []byte(`{}`), projection.Omit)
	fault, ok := AsFault(err, "")
	if !ok {
		t.Fatalf("expected fault error, got %v", err)
	}
	if fault.Reason != schema.ReasonUnsupportedHost {
		t.Errorf("reason = %q, want %q", fault.Reason, schema.ReasonUnsupportedHost)
	}
}

func TestDecodeFullProjectionCarriesCommand(t *testing.T) {
	raw := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`)
	event, err := Decode(adapters.Claude, raw, projection.Full)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if event.Content.Mode != schema.ContentFull || string(event.Content.Full) != `"ls"` {
		t.Errorf("content = %+v", event.Content)
	}
}

func TestRegisterCustomHostAndDecode(t *testing.T) {
	customHost := adapters.Host("custom-copilot")
	config := adapters.HostConfig{
		MainEventName: "preShellExec",
		Parser: adapters.ParserConfig{
			CanonicalEvent: []byte(`{"hook_event_name":"preShellExec","command":"echo hello"}`),
			CommandFor: func(raw adapters.RawHookEvent) (string, bool) {
				return raw.Command, raw.Command != ""
			},
			ActionKinds: map[string]string{
				"preShellExec": "shell",
			},
		},
		Partition: adapters.ControlPartition{
			Controllable: []string{"preShellExec"},
		},
	}

	err := adapters.RegisterHost(customHost, config)
	if err != nil {
		t.Fatalf("failed to register custom host: %v", err)
	}

	if !adapters.IsSupported(customHost) {
		t.Fatalf("custom host copilot should be supported")
	}

	rawPayload := []byte(`{"hook_event_name":"preShellExec","command":"echo hello"}`)
	event, err := Decode(customHost, rawPayload, projection.Full)
	if err != nil {
		t.Fatalf("decode custom host: %v", err)
	}

	if event.Host.Name != "custom-copilot" {
		t.Errorf("host name = %q, want custom-copilot", event.Host.Name)
	}
	if event.Action.Kind != "shell" {
		t.Errorf("action kind = %q, want shell", event.Action.Kind)
	}
	if event.Content.Mode != schema.ContentFull || string(event.Content.Full) != `"echo hello"` {
		t.Errorf("content = %+v", event.Content)
	}

	// Double registration of the same host must fail
	err = adapters.RegisterHost(customHost, config)
	if err == nil {
		t.Fatalf("expected error on duplicate host registration")
	}
}

func TestRegisterAsynchronousShellRejection(t *testing.T) {
	customHost := adapters.Host("async-agent")
	config := adapters.HostConfig{
		MainEventName: "notifyShell",
		Parser: adapters.ParserConfig{
			CanonicalEvent: []byte(`{"hook_event_name":"notifyShell","command":"echo hello"}`),
			CommandFor: func(raw adapters.RawHookEvent) (string, bool) {
				return raw.Command, raw.Command != ""
			},
			ActionKinds: map[string]string{
				"notifyShell": "shell",
			},
		},
		Partition: adapters.ControlPartition{
			// Rejection: The shell event is registered as uncontrollable (asynchronous, observation-only).
			Uncontrollable: []string{"notifyShell"},
		},
	}

	err := adapters.RegisterHost(customHost, config)
	if err == nil {
		t.Fatalf("expected registration error due to uncontrollable shell hook")
	}
	if !strings.Contains(err.Error(), "violates control mechanics") {
		t.Errorf("expected control violation error, got %v", err)
	}
}

func TestRegisterMissingShellRejection(t *testing.T) {
	customHost := adapters.Host("no-shell-agent")
	config := adapters.HostConfig{
		MainEventName: "beforeMCP",
		Parser: adapters.ParserConfig{
			CanonicalEvent: []byte(`{"hook_event_name":"beforeMCP","command":"run-tool"}`),
			CommandFor: func(raw adapters.RawHookEvent) (string, bool) {
				return raw.Command, raw.Command != ""
			},
			ActionKinds: map[string]string{
				"beforeMCP": "mcp",
			},
		},
		Partition: adapters.ControlPartition{
			// Rejection: Host has controllable MCP events, but no controllable shell event.
			Controllable: []string{"beforeMCP"},
		},
	}

	err := adapters.RegisterHost(customHost, config)
	if err == nil {
		t.Fatalf("expected registration error due to missing controllable shell hook")
	}
	if !strings.Contains(err.Error(), "requires at least one controllable (synchronous) shell boundary") {
		t.Errorf("expected control law error, got %v", err)
	}
}
