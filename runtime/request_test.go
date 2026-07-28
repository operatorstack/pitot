package runtime

import (
	"encoding/json"
	"testing"

	"github.com/operatorstack/pitot/schema"
)

func TestNewControlRequestFillsEnvelopeAndMarshalsPayload(t *testing.T) {
	type body struct {
		Actor     string `json:"actor"`
		Operation string `json:"operation"`
	}
	req, err := NewControlRequest("interlock.decide", body{Actor: "publisher", Operation: "artifact.publish"})
	if err != nil {
		t.Fatalf("NewControlRequest: %v", err)
	}
	if req.PitotVersion != schema.Version {
		t.Errorf("version = %q, want %q", req.PitotVersion, schema.Version)
	}
	if req.Type != schema.TypeControlRequested {
		t.Errorf("type = %q, want %q", req.Type, schema.TypeControlRequested)
	}
	if req.Kind != "interlock.decide" {
		t.Errorf("kind = %q, want interlock.decide", req.Kind)
	}
	if req.ActionID == "" {
		t.Error("ActionID was not minted")
	}
	var got body
	if err := json.Unmarshal(req.Data, &got); err != nil {
		t.Fatalf("data is not the marshaled payload: %v", err)
	}
	if got.Actor != "publisher" || got.Operation != "artifact.publish" {
		t.Errorf("round-tripped payload = %+v, want the input", got)
	}
}

func TestNewControlRequestRequiresKind(t *testing.T) {
	if _, err := NewControlRequest("", struct{}{}); err == nil {
		t.Fatal("empty kind must be an error")
	}
}

func TestNewControlRequestMintsDistinctActionIDs(t *testing.T) {
	a, err := NewControlRequest("k", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewControlRequest("k", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ActionID == b.ActionID {
		t.Fatalf("correlation ids must be unpredictable and distinct, got %q twice", a.ActionID)
	}
}
