package schema

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/operatorstack/pitot/protocol"
)

// MaxTokens bounds accounting to integers represented exactly by every SDK.
const MaxTokens = 9007199254740991

// DecodeEnvelope admits raw wire shape before typed decoding can hide missing,
// null, duplicate, differently-cased or unknown members. Controller data remains
// arbitrary JSON; only transport grammar is imposed on its contents.
func DecodeEnvelope(line []byte, target any) error {
	v, err := protocol.Parse(line)
	if err != nil {
		return protocol.ErrInvalidFrame
	}
	m, ok := v.(map[string]any)
	if !ok {
		return protocol.ErrInvalidFrame
	}
	valid := false
	var fresh any
	var accept func()
	switch dest := target.(type) {
	case *Event:
		if dest == nil {
			return protocol.ErrInvalidFrame
		}
		v := new(Event)
		fresh = v
		accept = func() { *dest = *v }
		valid = eventWire(m)
	case *ControlRequested:
		if dest == nil {
			return protocol.ErrInvalidFrame
		}
		v := new(ControlRequested)
		fresh = v
		accept = func() { *dest = *v }
		valid = object(m, "pitot_version type kind action_id", "data") && equal(m, "pitot_version", Version) && equal(m, "type", TypeControlRequested) && text(m, "kind", true) && text(m, "action_id", true)
	case *ControlResponse:
		if dest == nil {
			return protocol.ErrInvalidFrame
		}
		v := new(ControlResponse)
		fresh = v
		accept = func() { *dest = *v }
		valid = object(m, "pitot_version type controller_id action_id outcome", "message") && equal(m, "pitot_version", Version) && equal(m, "type", TypeControlResponse) && text(m, "controller_id", true) && text(m, "action_id", true) && oneOf(m, "outcome", OutcomeAllow, OutcomeDeny) && optionalText(m, "message")
	case *BoundaryFault:
		if dest == nil {
			return protocol.ErrInvalidFrame
		}
		v := new(BoundaryFault)
		fresh = v
		accept = func() { *dest = *v }
		valid = object(m, "pitot_version type host reason", "action_id") && equal(m, "pitot_version", Version) && equal(m, "type", TypeBoundaryFault) && text(m, "host", true) && optionalText(m, "action_id") && oneOf(m, "reason", ReasonEmptyCommand, ReasonMalformed, ReasonUnsupportedHost)
	}
	if !valid || json.Unmarshal(line, fresh) != nil {
		return protocol.ErrInvalidFrame
	}
	accept()
	return nil
}

func object(m map[string]any, required, optional string) bool {
	keys := map[string]bool{}
	for _, k := range strings.Fields(required) {
		if _, exists := m[k]; !exists {
			return false
		}
		keys[k] = true
	}
	for _, k := range strings.Fields(optional) {
		keys[k] = true
	}
	for k := range m {
		if !keys[k] {
			return false
		}
	}
	return true
}

func text(m map[string]any, k string, nonempty bool) bool {
	s, ok := m[k].(string)
	return ok && (!nonempty || s != "")
}

func optionalText(m map[string]any, k string) bool {
	_, exists := m[k]
	return !exists || text(m, k, false)
}

func equal(m map[string]any, k, wanted string) bool {
	s, ok := m[k].(string)
	return ok && s == wanted
}

func oneOf(m map[string]any, k string, values ...string) bool {
	for _, v := range values {
		if equal(m, k, v) {
			return true
		}
	}
	return false
}

func nested(m map[string]any, k string, required bool, check func(map[string]any) bool) bool {
	v, exists := m[k]
	if !exists {
		return !required
	}
	x, ok := v.(map[string]any)
	return ok && check(x)
}

func eventWire(m map[string]any) bool {
	if !object(m, "pitot_version type host observation", "id time session_id action content usage") || !equal(m, "pitot_version", Version) || !oneOf(m, "type", TypeActionRequested, TypeModelUsage) {
		return false
	}
	for _, k := range []string{"id", "time", "session_id"} {
		if !optionalText(m, k) {
			return false
		}
	}
	return nested(m, "host", true, func(h map[string]any) bool {
		return object(h, "name", "adapter_version") && text(h, "name", true) && optionalText(h, "adapter_version")
	}) && nested(m, "observation", true, func(o map[string]any) bool {
		return object(o, "source fidelity", "") && oneOf(o, "source", SourceHostHook, SourceHostEvent) && oneOf(o, "fidelity", FidelityDirect, FidelityEstimated)
	}) && nested(m, "action", equal(m, "type", TypeActionRequested), func(a map[string]any) bool {
		return object(a, "id kind", "") && text(a, "id", true) && text(a, "kind", true)
	}) && nested(m, "usage", equal(m, "type", TypeModelUsage), func(u map[string]any) bool {
		return object(u, "input_tokens output_tokens", "") && counter(u["input_tokens"]) && counter(u["output_tokens"])
	}) && nested(m, "content", false, contentWire)
}

func counter(v any) bool {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(string(n), ".eE") {
		return false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return err == nil && i >= 0 && i <= MaxTokens
}

func contentWire(c map[string]any) bool {
	switch c["mode"] {
	case ContentFull:
		return object(c, "mode full", "") && text(c, "full", false)
	case ContentOmit:
		return object(c, "mode", "")
	case ContentSHA256:
		s, ok := c["sha256"].(string)
		if !object(c, "mode sha256", "") || !ok || len(s) != 64 {
			return false
		}
		_, err := hex.DecodeString(s)
		return err == nil
	default:
		return false
	}
}
