// Package bridge routes synchronous control requests to a statically registered
// Controller and carries back exactly one correlated response.
//
// Synchronous hooks are control channels: the host is blocked waiting for an
// answer. The bridge makes that privilege explicit and mechanically enforces the
// declared default. It contains no approval, safety, completion, or shipping
// policy — those meanings belong to the Controller. The bridge is seeded from the
// Boatstack guard -> bootstrap-safety-hook contract (deny on timeout/unavailable,
// reject late/stale/duplicate/mismatched responses).
package bridge

import (
	"errors"
	"fmt"
	"sort"

	"github.com/operatorstack/pitot/schema"
)

// Registration is the static, auditable declaration binding one Controller to
// one request kind.
type Registration struct {
	Kind          string
	ControllerID  string
	DeadlineMS    int
	OnTimeout     string // schema.OutcomeAllow or schema.OutcomeDeny
	OnUnavailable string // schema.OutcomeAllow or schema.OutcomeDeny
}

func (r Registration) validate() error {
	if r.Kind == "" {
		return errors.New("pitot: registration requires a request kind")
	}
	if r.ControllerID == "" {
		return fmt.Errorf("pitot: registration for %q requires a controller id", r.Kind)
	}
	if r.DeadlineMS <= 0 {
		return fmt.Errorf("pitot: registration for %q requires a positive deadline", r.Kind)
	}
	if !validOutcome(r.OnTimeout) {
		return fmt.Errorf("pitot: registration for %q has invalid on_timeout %q", r.Kind, r.OnTimeout)
	}
	if !validOutcome(r.OnUnavailable) {
		return fmt.Errorf("pitot: registration for %q has invalid on_unavailable %q", r.Kind, r.OnUnavailable)
	}
	return nil
}

// Router holds at most one Controller registration per request kind.
type Router struct {
	registrations map[string]Registration
}

// NewRouter returns an empty Router.
func NewRouter() *Router {
	return &Router{registrations: map[string]Registration{}}
}

// Register records reg, enforcing the exactly-one-Controller-per-kind rule.
func (r *Router) Register(reg Registration) error {
	if err := reg.validate(); err != nil {
		return err
	}
	if _, exists := r.registrations[reg.Kind]; exists {
		return fmt.Errorf("pitot: a controller is already registered for kind %q", reg.Kind)
	}
	r.registrations[reg.Kind] = reg
	return nil
}

// Kinds returns the registered request kinds in stable order, for diagnostics.
func (r *Router) Kinds() []string {
	kinds := make([]string, 0, len(r.registrations))
	for kind := range r.registrations {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// Registration returns the registration for kind, if any.
func (r *Router) Registration(kind string) (Registration, bool) {
	reg, ok := r.registrations[kind]
	return reg, ok
}

// Terminal outcomes for a pending action.
var (
	// ErrNoController is returned when no Controller is registered for a kind.
	ErrNoController = errors.New("pitot: no controller registered for request kind")
	// ErrDuplicate is returned when a second response arrives for a resolved
	// action.
	ErrDuplicate = errors.New("pitot: pending action already resolved")
)

// Resolve validates a Controller's candidate response against the pending
// request and its registration, returning the single terminal resolution. A nil
// candidate means the Controller was unavailable; a candidate that fails
// correlation is rejected and the declared default applies.
func (r *Router) Resolve(req schema.ControlRequested, candidate *schema.ControlResponse) (schema.ControlResponse, error) {
	reg, ok := r.registrations[req.Kind]
	if !ok {
		return schema.ControlResponse{}, ErrNoController
	}
	if candidate == nil {
		return r.defaultResponse(reg, req, reg.OnUnavailable), nil
	}
	if err := validateResponse(reg, req, *candidate); err != nil {
		// A mismatched, stale, or malformed response is rejected; the pending
		// action still receives exactly one terminal resolution via the default.
		return r.defaultResponse(reg, req, reg.OnUnavailable), err
	}
	return schema.ControlResponse{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlResponse,
		ControllerID: reg.ControllerID,
		ActionID:     req.ActionID,
		Outcome:      candidate.Outcome,
		Message:      candidate.Message,
	}, nil
}

// TimeoutResponse returns the declared default resolution when the deadline
// elapsed before the Controller answered.
func (r *Router) TimeoutResponse(req schema.ControlRequested) (schema.ControlResponse, error) {
	reg, ok := r.registrations[req.Kind]
	if !ok {
		return schema.ControlResponse{}, ErrNoController
	}
	return r.defaultResponse(reg, req, reg.OnTimeout), nil
}

func (r *Router) defaultResponse(reg Registration, req schema.ControlRequested, outcome string) schema.ControlResponse {
	return schema.ControlResponse{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlResponse,
		ControllerID: reg.ControllerID,
		ActionID:     req.ActionID,
		Outcome:      outcome,
	}
}

func validateResponse(reg Registration, req schema.ControlRequested, resp schema.ControlResponse) error {
	if resp.PitotVersion != schema.Version {
		return fmt.Errorf("pitot: response for %q has unsupported version %q", req.Kind, resp.PitotVersion)
	}
	if resp.Type != schema.TypeControlResponse {
		return fmt.Errorf("pitot: response for %q has unexpected type %q", req.Kind, resp.Type)
	}
	if resp.ControllerID != reg.ControllerID {
		return fmt.Errorf("pitot: response for %q came from unregistered controller %q", req.Kind, resp.ControllerID)
	}
	if resp.ActionID != req.ActionID {
		return fmt.Errorf("pitot: response action id %q does not match pending action %q", resp.ActionID, req.ActionID)
	}
	if !validOutcome(resp.Outcome) {
		return fmt.Errorf("pitot: response for %q has invalid outcome %q", req.Kind, resp.Outcome)
	}
	return nil
}

func validOutcome(outcome string) bool {
	return outcome == schema.OutcomeAllow || outcome == schema.OutcomeDeny
}
