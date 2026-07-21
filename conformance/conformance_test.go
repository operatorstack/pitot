package conformance

import (
	"testing"

	"github.com/operatorstack/pitot/adapters"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
	"github.com/operatorstack/pitot/sensor"
)

func TestPositiveFixturesDecode(t *testing.T) {
	cases, err := Positive()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no positive fixtures loaded")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			event, err := sensor.Decode(adapters.Host(c.Host), c.Payload(), projection.Mode(c.Mode))
			if err != nil {
				t.Fatalf("expected decode, got fault: %v", err)
			}
			if event.Observation.Source != schema.SourceHostHook || event.Observation.Fidelity != schema.FidelityDirect {
				t.Errorf("observation = %+v", event.Observation)
			}
			if c.ExpectKind != "" && (event.Action == nil || event.Action.Kind != c.ExpectKind) {
				t.Errorf("action kind = %+v, want %q", event.Action, c.ExpectKind)
			}
			if event.Content == nil || event.Content.Mode != c.Mode {
				t.Errorf("content mode = %+v, want %q", event.Content, c.Mode)
			}
		})
	}
}

func TestNegativeFixturesFault(t *testing.T) {
	cases, err := Negative()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no negative fixtures loaded")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			_, err := sensor.Decode(adapters.Host(c.Host), c.Payload(), projection.Mode(c.Mode))
			fault, ok := sensor.AsFault(err, "act_test")
			if !ok {
				t.Fatalf("expected boundary fault, got %v", err)
			}
			if fault.Reason != c.Reason {
				t.Errorf("reason = %q, want %q", fault.Reason, c.Reason)
			}
			if fault.Type != schema.TypeBoundaryFault {
				t.Errorf("fault type = %q", fault.Type)
			}
		})
	}
}
