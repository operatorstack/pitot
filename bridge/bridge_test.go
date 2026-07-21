package bridge

import (
	"testing"

	"github.com/operatorstack/pitot/schema"
)

func reg() Registration {
	return Registration{
		Kind:          "release.approval",
		ControllerID:  "local-approval",
		DeadlineMS:    2000,
		OnTimeout:     schema.OutcomeDeny,
		OnUnavailable: schema.OutcomeDeny,
	}
}

func request() schema.ControlRequested {
	return schema.ControlRequested{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlRequested,
		Kind:         "release.approval",
		ActionID:     "act_7f2",
	}
}

func TestRegisterEnforcesSingleController(t *testing.T) {
	r := NewRouter()
	if err := r.Register(reg()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(reg()); err == nil {
		t.Fatal("expected duplicate registration to be rejected")
	}
}

func TestResolveValidResponse(t *testing.T) {
	r := NewRouter()
	_ = r.Register(reg())
	candidate := &schema.ControlResponse{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlResponse,
		ControllerID: "local-approval",
		ActionID:     "act_7f2",
		Outcome:      schema.OutcomeAllow,
		Message:      "approved",
	}
	resp, err := r.Resolve(request(), candidate)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resp.Outcome != schema.OutcomeAllow || resp.ActionID != "act_7f2" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestResolveMismatchedActionIsRejected(t *testing.T) {
	r := NewRouter()
	_ = r.Register(reg())
	candidate := &schema.ControlResponse{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlResponse,
		ControllerID: "local-approval",
		ActionID:     "act_OTHER",
		Outcome:      schema.OutcomeAllow,
	}
	resp, err := r.Resolve(request(), candidate)
	if err == nil {
		t.Fatal("expected mismatched action id to be rejected")
	}
	if resp.Outcome != schema.OutcomeDeny {
		t.Errorf("rejected response should fall back to deny default, got %+v", resp)
	}
}

func TestResolveWrongControllerIsRejected(t *testing.T) {
	r := NewRouter()
	_ = r.Register(reg())
	candidate := &schema.ControlResponse{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlResponse,
		ControllerID: "impostor",
		ActionID:     "act_7f2",
		Outcome:      schema.OutcomeAllow,
	}
	if _, err := r.Resolve(request(), candidate); err == nil {
		t.Fatal("expected unregistered controller to be rejected")
	}
}

func TestResolveUnavailableUsesDefault(t *testing.T) {
	r := NewRouter()
	_ = r.Register(reg())
	resp, err := r.Resolve(request(), nil)
	if err != nil {
		t.Fatalf("resolve unavailable: %v", err)
	}
	if resp.Outcome != schema.OutcomeDeny {
		t.Errorf("unavailable outcome = %q, want deny", resp.Outcome)
	}
}

func TestTimeoutUsesDeclaredDefault(t *testing.T) {
	r := NewRouter()
	_ = r.Register(reg())
	resp, err := r.TimeoutResponse(request())
	if err != nil {
		t.Fatalf("timeout: %v", err)
	}
	if resp.Outcome != schema.OutcomeDeny {
		t.Errorf("timeout outcome = %q, want deny", resp.Outcome)
	}
}

func TestResolveNoControllerFails(t *testing.T) {
	r := NewRouter()
	if _, err := r.Resolve(request(), nil); err != ErrNoController {
		t.Fatalf("err = %v, want ErrNoController", err)
	}
}
