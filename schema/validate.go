package schema

import (
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Validation errors contain no envelope identifiers, payloads or messages.
var (
	ErrInvalidContent  = errors.New("pitot: invalid content representation")
	ErrInvalidEvent    = errors.New("pitot: invalid event envelope")
	ErrInvalidRequest  = errors.New("pitot: invalid control request envelope")
	ErrInvalidResponse = errors.New("pitot: invalid control response envelope")
	ErrInvalidFault    = errors.New("pitot: invalid boundary fault envelope")
)

// ValidateContent checks the mutually exclusive representations emitted by
// projection.Apply. Full content is a JSON string containing the measured bytes.
func ValidateContent(content Content) error {
	switch content.Mode {
	case ContentFull:
		var value any
		if content.SHA256 != "" || json.Unmarshal(content.Full, &value) != nil {
			return ErrInvalidContent
		}
		if _, ok := value.(string); !ok {
			return ErrInvalidContent
		}
	case ContentSHA256:
		if len(content.Full) != 0 || len(content.SHA256) != 64 {
			return ErrInvalidContent
		}
		if _, err := hex.DecodeString(content.SHA256); err != nil {
			return ErrInvalidContent
		}
	case ContentOmit:
		if len(content.Full) != 0 || content.SHA256 != "" {
			return ErrInvalidContent
		}
	default:
		return ErrInvalidContent
	}
	return nil
}

// ValidateEvent checks typed v1 event semantics. Transport-specific admission
// remains separate: recognizing model.usage does not enable runtime delivery.
func ValidateEvent(event Event) error {
	if event.PitotVersion != Version || event.Host.Name == "" {
		return ErrInvalidEvent
	}
	if event.Observation.Source != SourceHostHook && event.Observation.Source != SourceHostEvent {
		return ErrInvalidEvent
	}
	if event.Observation.Fidelity != FidelityDirect && event.Observation.Fidelity != FidelityEstimated {
		return ErrInvalidEvent
	}
	if event.Action != nil && (event.Action.ID == "" || event.Action.Kind == "") {
		return ErrInvalidEvent
	}
	if event.Usage != nil && (event.Usage.InputTokens < 0 || event.Usage.OutputTokens < 0) {
		return ErrInvalidEvent
	}
	switch event.Type {
	case TypeActionRequested:
		if event.Action == nil {
			return ErrInvalidEvent
		}
	case TypeModelUsage:
		if event.Usage == nil {
			return ErrInvalidEvent
		}
	default:
		return ErrInvalidEvent
	}
	if event.Content != nil && ValidateContent(*event.Content) != nil {
		return ErrInvalidEvent
	}
	return nil
}

// ValidateRequest leaves Data under the Controller's authority. Any valid JSON
// value, including null, is admissible; an absent value is also valid.
func ValidateRequest(request ControlRequested) error {
	if request.PitotVersion != Version || request.Type != TypeControlRequested || request.Kind == "" || request.ActionID == "" || (len(request.Data) != 0 && !json.Valid(request.Data)) {
		return ErrInvalidRequest
	}
	return nil
}

// ValidateResponse checks envelope semantics; pending-action correlation is
// enforced separately by the bridge against its registration.
func ValidateResponse(response ControlResponse) error {
	if response.PitotVersion != Version || response.Type != TypeControlResponse || response.ControllerID == "" || response.ActionID == "" || (response.Outcome != OutcomeAllow && response.Outcome != OutcomeDeny) {
		return ErrInvalidResponse
	}
	return nil
}

func ValidateFault(fault BoundaryFault) error {
	if fault.PitotVersion != Version || fault.Type != TypeBoundaryFault || fault.Host == "" {
		return ErrInvalidFault
	}
	switch fault.Reason {
	case ReasonEmptyCommand, ReasonMalformed, ReasonUnsupportedHost:
		return nil
	default:
		return ErrInvalidFault
	}
}
