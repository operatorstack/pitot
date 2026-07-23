// Package adapters declares the coding-agent host boundaries Pitot supports and
// the host-specific hook shapes it understands.
//
// The host mapping is seeded from the Boatstack safety-hook adapters
// (labs/12-product-engineering-loop/product-engineering-loop/hooks.go:
// hookEvents, canonicalHookEvent, desiredHostHookForEvent). An adapter declares
// its host capabilities and the canonical read-only probe event; the sensor
// package owns turning raw host payloads into normalized schema.Event values.
//
// This package deliberately does not import bridge/: adapters describe what a
// host can report, never what a controller decides.
package adapters

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Host is a supported coding-agent host identifier.
type Host string

// Supported hosts.
const (
	Cursor   Host = "cursor"
	Claude   Host = "claude"
	Codex    Host = "codex"
	Gemini   Host = "gemini"
	Opencode Host = "opencode"
	Kimi     Host = "kimi"
	Copilot  Host = "copilot"
	Qwen     Host = "qwen"
	Pi       Host = "pi"
)

// AdapterVersion is the semantic version stamped onto normalized events so
// consumers can reason about host-compatibility differences.
const AdapterVersion = "0.1.0"

// ParserConfig handles regular coding/parsing chores, converting raw host fields
// into normalized action types and command strings.
type ParserConfig struct {
	CanonicalEvent []byte
	EventNameFor   func(raw RawHookEvent) string
	CommandFor     func(raw RawHookEvent) (string, bool)
	ActionKinds    map[string]string // hook event name -> normalized action kind ("shell", "mcp")
}

// ControlPartition declares the physics of the host's hook boundaries.
// In supervisory control theory (supervisory-control.md), this defines the
// control partition (Σ = Σ_c ∪ Σ_u).
type ControlPartition struct {
	// Controllable (Σ_c): Synchronous, blocking hook events where the host
	// pauses execution and waits for a permission response. The supervisor
	// can safely disable these.
	Controllable []string

	// Uncontrollable (Σ_u): Asynchronous, fire-and-forget hook events where
	// the host reports behavior but cannot be prevented or paused. These are
	// observation-only.
	Uncontrollable []string
}

// HostConfig unites the regular parser with the mathematical plant physics.
type HostConfig struct {
	MainEventName string
	Parser        ParserConfig
	Partition     ControlPartition
}

var (
	registryMu sync.RWMutex
	registry   = map[Host]HostConfig{
		Copilot: preToolUseHost(),
		Qwen: {
			MainEventName: "PreToolUse",
			Parser: ParserConfig{
				CanonicalEvent: []byte(`{"hook_event_name":"PreToolUse","tool_name":"run_shell_command","tool_input":{"command":"git status --short"}}`),
				CommandFor: func(raw RawHookEvent) (string, bool) {
					if raw.ToolName != "Bash" && raw.ToolName != "run_shell_command" {
						return "", false
					}
					return toolInputCommand(raw)
				},
				ActionKinds: map[string]string{"PreToolUse": "shell"},
			},
			Partition: ControlPartition{Controllable: []string{"PreToolUse"}},
		},
		Pi: {
			MainEventName: "tool_call",
			Parser: ParserConfig{
				CanonicalEvent: []byte(`{"hook_event_name":"tool_call","tool_name":"bash","tool_input":{"command":"git status --short"}}`),
				CommandFor:     toolInputCommand,
				ActionKinds:    map[string]string{"tool_call": "shell"},
			},
			Partition: ControlPartition{Controllable: []string{"tool_call"}},
		},
		Cursor: {
			MainEventName: "beforeShellExecution",
			Parser: ParserConfig{
				CanonicalEvent: []byte(`{"hook_event_name":"beforeShellExecution","command":"git status --short"}`),
				CommandFor: func(raw RawHookEvent) (string, bool) {
					return raw.Command, raw.Command != ""
				},
				ActionKinds: map[string]string{
					"beforeMCPExecution":   "mcp",
					"beforeShellExecution": "shell",
				},
			},
			Partition: ControlPartition{
				// In Cursor, both shell and MCP hooks are synchronous (blocking).
				Controllable: []string{"beforeShellExecution", "beforeMCPExecution"},
			},
		},
		Claude: {
			MainEventName: "PreToolUse",
			Parser: ParserConfig{
				CanonicalEvent: []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`),
				CommandFor: func(raw RawHookEvent) (string, bool) {
					if raw.ToolInput == nil {
						return "", false
					}
					value, present := raw.ToolInput["command"].(string)
					return value, present && value != ""
				},
				ActionKinds: map[string]string{
					"PreToolUse": "shell",
				},
			},
			Partition: ControlPartition{
				// In Claude, the PreToolUse hook is synchronous (blocking).
				Controllable: []string{"PreToolUse"},
			},
		},
		Codex: {
			MainEventName: "PreToolUse",
			Parser: ParserConfig{
				CanonicalEvent: []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`),
				CommandFor: func(raw RawHookEvent) (string, bool) {
					if raw.ToolInput == nil {
						return "", false
					}
					value, present := raw.ToolInput["command"].(string)
					return value, present && value != ""
				},
				ActionKinds: map[string]string{
					"PreToolUse": "shell",
				},
			},
			Partition: ControlPartition{
				// In Codex, the PreToolUse hook is synchronous (blocking).
				Controllable: []string{"PreToolUse"},
			},
		},
		Gemini: {
			MainEventName: "BeforeTool",
			Parser: ParserConfig{
				CanonicalEvent: []byte(`{"hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"git status --short"}}`),
				CommandFor: func(raw RawHookEvent) (string, bool) {
					if raw.ToolInput == nil {
						return "", false
					}
					value, present := raw.ToolInput["command"].(string)
					return value, present && value != ""
				},
				ActionKinds: map[string]string{
					"BeforeTool": "shell",
				},
			},
			Partition: ControlPartition{
				// In Gemini, the BeforeTool hook is synchronous (blocking).
				Controllable: []string{"BeforeTool"},
			},
		},
		Opencode: {
			MainEventName: "PreToolUse",
			Parser: ParserConfig{
				CanonicalEvent: []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`),
				CommandFor: func(raw RawHookEvent) (string, bool) {
					if raw.ToolInput == nil {
						return "", false
					}
					value, present := raw.ToolInput["command"].(string)
					return value, present && value != ""
				},
				ActionKinds: map[string]string{
					"PreToolUse": "shell",
				},
			},
			Partition: ControlPartition{
				// In Opencode, the PreToolUse hook is synchronous (blocking).
				Controllable: []string{"PreToolUse"},
			},
		},
		Kimi: {
			MainEventName: "PreToolUse",
			Parser: ParserConfig{
				CanonicalEvent: []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`),
				CommandFor: func(raw RawHookEvent) (string, bool) {
					if raw.ToolInput == nil {
						return "", false
					}
					value, present := raw.ToolInput["command"].(string)
					return value, present && value != ""
				},
				ActionKinds: map[string]string{
					"PreToolUse": "shell",
				},
			},
			Partition: ControlPartition{
				// Kimi Code's PreToolUse hook is synchronous and blocking.
				Controllable: []string{"PreToolUse"},
			},
		},
	}
)

func toolInputCommand(raw RawHookEvent) (string, bool) {
	value, present := raw.ToolInput["command"].(string)
	return value, present && value != ""
}

func preToolUseHost() HostConfig {
	return HostConfig{
		MainEventName: "PreToolUse",
		Parser: ParserConfig{
			CanonicalEvent: []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status --short"}}`),
			CommandFor: func(raw RawHookEvent) (string, bool) {
				if raw.ToolName != "Bash" {
					return "", false
				}
				return toolInputCommand(raw)
			},
			ActionKinds: map[string]string{"PreToolUse": "shell"},
		},
		Partition: ControlPartition{Controllable: []string{"PreToolUse"}},
	}
}

// RegisterHost registers a custom host with its configuration, enforcing
// that it complies with the required supervisory control laws.
func RegisterHost(h Host, config HostConfig) error {
	registryMu.Lock()
	defer registryMu.Unlock()

	if h == "" {
		return errors.New("pitot: host name cannot be empty")
	}
	if len(config.Partition.Controllable) == 0 && len(config.Partition.Uncontrollable) == 0 {
		return fmt.Errorf("pitot: host %q must declare at least one controllable or uncontrollable event", h)
	}
	if config.MainEventName == "" {
		return fmt.Errorf("pitot: host %q must declare a main event name", h)
	}
	if len(config.Parser.CanonicalEvent) == 0 {
		return fmt.Errorf("pitot: host %q must declare a canonical event payload", h)
	}
	if config.Parser.CommandFor == nil {
		return fmt.Errorf("pitot: host %q must declare a CommandFor extractor", h)
	}

	// --- Control Laws Verification ---
	// To safely supervise a host (maintain nonblocking control under K),
	// the host MUST provide at least one controllable (synchronous, blocking) shell hook.
	hasControllableShell := false
	for _, eventName := range config.Partition.Controllable {
		if config.Parser.ActionKinds[eventName] == "shell" {
			hasControllableShell = true
			break
		}
	}

	if !hasControllableShell {
		return fmt.Errorf("pitot: host %q violates control mechanics: requires at least one controllable (synchronous) shell boundary to be supervised", h)
	}

	if _, exists := registry[h]; exists {
		return fmt.Errorf("pitot: host %q is already registered", h)
	}

	registry[h] = config
	return nil
}

// Supported returns the hosts this build can normalize, in stable order.
func Supported() []Host {
	registryMu.RLock()
	defer registryMu.RUnlock()

	hosts := make([]Host, 0, len(registry))
	for h := range registry {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		return hosts[i] < hosts[j]
	})
	return hosts
}

// IsSupported reports whether h is a recognized host.
func IsSupported(h Host) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()

	_, exists := registry[h]
	return exists
}

// HookEvents returns the host hook event names Pitot attaches to.
func HookEvents(h Host) []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	if config, exists := registry[h]; exists {
		events := make([]string, 0, len(config.Partition.Controllable)+len(config.Partition.Uncontrollable))
		events = append(events, config.Partition.Controllable...)
		events = append(events, config.Partition.Uncontrollable...)
		return events
	}
	return nil
}

// HookEventName is the field each host uses to name its hook event.
func HookEventName(h Host) (string, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	if config, exists := registry[h]; exists {
		return config.MainEventName, nil
	}
	return "", fmt.Errorf("pitot: unsupported hook host %q", h)
}

// CanonicalHookEvent returns a canonical, read-only probe payload for a host.
func CanonicalHookEvent(h Host) ([]byte, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	if config, exists := registry[h]; exists {
		payload := make([]byte, len(config.Parser.CanonicalEvent))
		copy(payload, config.Parser.CanonicalEvent)
		return payload, nil
	}
	return nil, fmt.Errorf("pitot: unsupported hook host %q", h)
}

// RawHookEvent is the union of fields Pitot reads from a host hook payload. The
// sensor decodes into this shape before normalizing; adapters preserve host
// capability differences rather than inventing missing fields.
type RawHookEvent struct {
	// HookEventName is the host's event discriminator.
	HookEventName string `json:"hook_event_name"`
	// Command is populated by Cursor's beforeShellExecution.
	Command string `json:"command"`
	// ToolName / ToolInput are populated by PreToolUse-style hosts and Gemini.
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
}

// EventNameFor extracts the host-specific event discriminator.
func (h Host) EventNameFor(raw RawHookEvent) string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if config, exists := registry[h]; exists && config.Parser.EventNameFor != nil {
		return config.Parser.EventNameFor(raw)
	}
	return raw.HookEventName
}

// CommandFor extracts the shell command a raw hook event describes, per host
// shape. It returns ok=false when the host omitted the command entirely, so the
// sensor can raise a boundary fault instead of manufacturing an empty command.
func (h Host) CommandFor(raw RawHookEvent) (command string, ok bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	if config, exists := registry[h]; exists {
		return config.Parser.CommandFor(raw)
	}
	return "", false
}

// HasHookEvent reports whether the host supports the given hook event name.
func (h Host) HasHookEvent(name string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()

	if config, exists := registry[h]; exists {
		for _, e := range config.Partition.Controllable {
			if e == name {
				return true
			}
		}
		for _, e := range config.Partition.Uncontrollable {
			if e == name {
				return true
			}
		}
	}
	return false
}

// ActionKind returns the normalized action kind (e.g., "shell", "mcp") for a given hook event name.
func (h Host) ActionKind(hookEventName string) string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	if config, exists := registry[h]; exists {
		if kind, ok := config.Parser.ActionKinds[hookEventName]; ok {
			return kind
		}
	}
	return "shell" // fallback default
}
