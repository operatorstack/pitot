// Package schema defines Pitot's public event, control, and fault envelopes.
//
// These types are the language-neutral contract expressed in Go. The canonical
// definition is the versioned JSON Schema corpus; this package mirrors it so the
// reference implementation and its fixtures stay in lockstep. Field names and
// omitempty rules are part of the wire contract and must not drift casually.
package schema

import "encoding/json"

// Version is the current Pitot envelope version carried on every message.
const Version = "1"

// Message type discriminators (the "type" field).
const (
	TypeActionRequested  = "action.requested"
	TypeModelUsage       = "model.usage"
	TypeControlRequested = "control.requested"
	TypeControlResponse  = "control.response"
	TypeBoundaryFault    = "boundary.fault"
)

// Observation source values.
const (
	SourceHostHook  = "host_hook"
	SourceHostEvent = "host_event"
)

// Observation fidelity values. A normalized field is never presented as directly
// observed when the host omitted it or Pitot inferred it.
const (
	FidelityDirect    = "direct"
	FidelityEstimated = "estimated"
)

// Content projection modes.
const (
	ContentFull   = "full"
	ContentSHA256 = "sha256"
	ContentOmit   = "omit"
)

// Control outcomes.
const (
	OutcomeAllow = "allow"
	OutcomeDeny  = "deny"
)

// Host identifies the coding-agent host that produced an event and the adapter
// version that normalized it.
type Host struct {
	Name           string `json:"name"`
	AdapterVersion string `json:"adapter_version,omitempty"`
}

// Action identifies the pending host action an event or control message is bound
// to.
type Action struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// Content carries the projected representation of a payload. Exactly one of the
// Full/SHA256 representations is populated according to Mode; Omit populates
// neither.
type Content struct {
	Mode   string          `json:"mode"`
	SHA256 string          `json:"sha256,omitempty"`
	Full   json.RawMessage `json:"full,omitempty"`
}

// Observation records where a fact came from and how directly it was measured.
type Observation struct {
	Source   string `json:"source"`
	Fidelity string `json:"fidelity"`
}

// Event is the normalized observation envelope delivered to Consumers.
type Event struct {
	PitotVersion string      `json:"pitot_version"`
	ID           string      `json:"id,omitempty"`
	Type         string      `json:"type"`
	Time         string      `json:"time,omitempty"`
	Host         Host        `json:"host"`
	SessionID    string      `json:"session_id,omitempty"`
	Action       *Action     `json:"action,omitempty"`
	Content      *Content    `json:"content,omitempty"`
	Usage        *Usage      `json:"usage,omitempty"`
	Observation  Observation `json:"observation"`
}

// Usage carries token accounting for model.usage events.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ControlRequested is delivered to the registered Controller for a request kind.
type ControlRequested struct {
	PitotVersion string          `json:"pitot_version"`
	Type         string          `json:"type"`
	Kind         string          `json:"kind"`
	ActionID     string          `json:"action_id"`
	Data         json.RawMessage `json:"data,omitempty"`
}

// ControlResponse is the single correlated answer a Controller returns for a
// pending action.
type ControlResponse struct {
	PitotVersion string `json:"pitot_version"`
	Type         string `json:"type"`
	ControllerID string `json:"controller_id"`
	ActionID     string `json:"action_id"`
	Outcome      string `json:"outcome"`
	Message      string `json:"message,omitempty"`
}

// BoundaryFault reports a broken measurement boundary without exposing content.
// Reason codes never contain prompts, commands, tool inputs, or outputs.
type BoundaryFault struct {
	PitotVersion string `json:"pitot_version"`
	Type         string `json:"type"`
	Host         string `json:"host"`
	ActionID     string `json:"action_id,omitempty"`
	Reason       string `json:"reason"`
}

// Boundary fault reason codes.
const (
	ReasonEmptyCommand    = "empty-command"
	ReasonMalformed       = "malformed-event"
	ReasonUnsupportedHost = "unsupported-host"
)
