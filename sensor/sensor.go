// Package sensor normalizes raw host hook payloads into Pitot's event envelope.
//
// It is the observation pipeline: it reports what a host supplied and marks the
// quality of that observation. It never decides what an action means and it must
// never import bridge/ — measurement is innocent of control. That invariant is
// enforced at build time by scripts/check_import_boundary.sh.
package sensor

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
)

// FaultError reports a broken measurement boundary. The bridge/runtime converts
// it into a schema.BoundaryFault; the reason code is content-safe and never
// carries the offending command or payload.
type FaultError struct {
	Host   adapters.Host
	Reason string
}

func (e *FaultError) Error() string {
	return fmt.Sprintf("pitot: boundary fault for host %q: %s", e.Host, e.Reason)
}

// Fault builds the content-safe BoundaryFault envelope for a FaultError.
func (e *FaultError) Fault(actionID string) schema.BoundaryFault {
	return schema.BoundaryFault{
		PitotVersion: schema.Version,
		Type:         schema.TypeBoundaryFault,
		Host:         string(e.Host),
		ActionID:     actionID,
		Reason:       e.Reason,
	}
}

// Decode normalizes a raw host hook payload into an action.requested event with
// the requested content projection. A malformed payload or an omitted command
// yields a *FaultError so the caller can emit a boundary fault rather than a
// silently degraded event.
func Decode(host adapters.Host, raw []byte, mode projection.Mode) (schema.Event, error) {
	if !adapters.IsSupported(host) {
		return schema.Event{}, &FaultError{Host: host, Reason: schema.ReasonUnsupportedHost}
	}
	if !mode.Valid() {
		return schema.Event{}, fmt.Errorf("pitot: unsupported projection mode %q", mode)
	}

	var event adapters.RawHookEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return schema.Event{}, &FaultError{Host: host, Reason: schema.ReasonMalformed}
	}

	// An empty or mismatched event name is a malformed boundary, not a decision.
	if event.HookEventName != "" && !host.HasHookEvent(event.HookEventName) {
		return schema.Event{}, &FaultError{Host: host, Reason: schema.ReasonMalformed}
	}

	command, ok := host.CommandFor(event)
	if !ok {
		return schema.Event{}, &FaultError{Host: host, Reason: schema.ReasonEmptyCommand}
	}

	content, err := projection.Apply(mode, []byte(command))
	if err != nil {
		return schema.Event{}, err
	}

	return schema.Event{
		PitotVersion: schema.Version,
		Type:         schema.TypeActionRequested,
		Host: schema.Host{
			Name:           string(host),
			AdapterVersion: adapters.AdapterVersion,
		},
		Action:  &schema.Action{Kind: host.ActionKind(event.HookEventName)},
		Content: &content,
		Observation: schema.Observation{
			Source:   schema.SourceHostHook,
			Fidelity: schema.FidelityDirect,
		},
	}, nil
}

// AsFault returns the BoundaryFault for err when err is a sensor FaultError.
func AsFault(err error, actionID string) (schema.BoundaryFault, bool) {
	var fault *FaultError
	if errors.As(err, &fault) {
		return fault.Fault(actionID), true
	}
	return schema.BoundaryFault{}, false
}
