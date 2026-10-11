package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/operatorstack/pitot/conformance"
	"github.com/operatorstack/pitot/schema"
)

func TestSharedEnvelopeSemantics(t *testing.T) {
	cases, err := conformance.Envelopes()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var value any
			var validate func() error
			switch c.Envelope {
			case "event":
				v := &schema.Event{}
				value = v
				validate = func() error { return schema.ValidateEvent(*v) }
			case "request":
				v := &schema.ControlRequested{}
				value = v
				validate = func() error { return schema.ValidateRequest(*v) }
			case "response":
				v := &schema.ControlResponse{}
				value = v
				validate = func() error { return schema.ValidateResponse(*v) }
			case "fault":
				v := &schema.BoundaryFault{}
				value = v
				validate = func() error { return schema.ValidateFault(*v) }
			default:
				t.Fatalf("unknown fixture envelope %q", c.Envelope)
			}
			err := json.Unmarshal(c.Message, value)
			if err == nil {
				err = validate()
			}
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v, error=%v", c.Valid, err)
			}
		})
	}
}

func TestInvalidRawRepresentationsAreContentSafe(t *testing.T) {
	content := schema.Content{Mode: schema.ContentFull, Full: json.RawMessage(`SAMPLE_PAYLOAD`)}
	if err := schema.ValidateContent(content); err != schema.ErrInvalidContent {
		t.Fatalf("error=%v", err)
	}
	request := schema.ControlRequested{PitotVersion: schema.Version, Type: schema.TypeControlRequested, Kind: "custom", ActionID: "act", Data: json.RawMessage(`SAMPLE_PAYLOAD`)}
	if err := schema.ValidateRequest(request); err != schema.ErrInvalidRequest {
		t.Fatalf("error=%v", err)
	}
}
