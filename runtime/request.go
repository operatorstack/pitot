package runtime

// Typed request construction. A caller issuing an explicit control request would
// otherwise hand-build the control.requested envelope: set the version and type
// discriminators, mint a correlation id, and marshal the structured body into the
// raw `data` field itself. NewControlRequest and RequestTyped remove that JSON
// boundary — the caller passes a typed payload and gets a correlated response.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/operatorstack/pitot/schema"
)

// NewControlRequest builds a control.requested envelope for the given request
// kind, marshaling payload into the data field. It stamps the current envelope
// version and control.requested type and mints an unpredictable correlation
// ActionID, so a caller never hand-writes the wire JSON or the boilerplate. A nil
// payload produces an empty data field (a request that carries no structured
// body); a payload that cannot be marshaled is an error.
func NewControlRequest(kind string, payload any) (schema.ControlRequested, error) {
	if kind == "" {
		return schema.ControlRequested{}, errors.New("pitot: control request kind is required")
	}
	var data json.RawMessage
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return schema.ControlRequested{}, fmt.Errorf("pitot: marshal control request data: %w", err)
		}
		data = encoded
	}
	actionID, err := NewActionID()
	if err != nil {
		return schema.ControlRequested{}, err
	}
	return schema.ControlRequested{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlRequested,
		Kind:         kind,
		ActionID:     actionID,
		Data:         data,
	}, nil
}

// RequestTyped builds a typed control request for kind and issues it, returning
// the correlated response. It is the typed convenience over Request: the caller
// supplies a structured payload rather than a pre-framed envelope with raw JSON.
// All of Request's guarantees still apply — correlation, the client deadline, and
// the runtime's fail-closed unavailable/timeout defaults.
func (c *Client) RequestTyped(ctx context.Context, kind string, payload any) (schema.ControlResponse, error) {
	req, err := NewControlRequest(kind, payload)
	if err != nil {
		return schema.ControlResponse{}, err
	}
	return c.Request(ctx, req)
}
