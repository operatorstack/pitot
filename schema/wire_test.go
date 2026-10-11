package schema_test

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/operatorstack/pitot/schema"
)

func TestDecodeReplacesReusedTargetOnlyOnAcceptance(t *testing.T) {
	request := schema.ControlRequested{Data: json.RawMessage(`{"old_approval":true}`)}
	raw := []byte(`{"pitot_version":"1","type":"control.requested","kind":"test","action_id":"new"}`)
	if err := schema.DecodeEnvelope(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.Data != nil {
		t.Fatal("absent data retained from previous request")
	}
	if err := schema.DecodeEnvelope([]byte(`{"kind":null}`), &request); err == nil {
		t.Fatal("invalid input accepted")
	}
	if request.ActionID != "new" {
		t.Fatal("rejected candidate changed target")
	}
	var nilRequest *schema.ControlRequested
	if err := schema.DecodeEnvelope(raw, nilRequest); err == nil {
		t.Fatal("nil target accepted")
	}
}

func TestSharedRawWireCorpus(t *testing.T) {
	f, err := os.Open("../conformance/fixtures/wire.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 4*1024*1024)
	for s.Scan() {
		var row struct {
			Name     string `json:"name"`
			Envelope string `json:"envelope"`
			Valid    bool   `json:"valid"`
			Raw      string `json:"raw"`
			RawHex   string `json:"raw_hex"`
		}
		if err := json.Unmarshal(s.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		t.Run(row.Name, func(t *testing.T) {
			raw := []byte(row.Raw)
			if row.RawHex != "" {
				var err error
				raw, err = hex.DecodeString(row.RawHex)
				if err != nil {
					t.Fatal(err)
				}
			}
			var target any
			switch row.Envelope {
			case "event":
				target = new(schema.Event)
			case "request":
				target = new(schema.ControlRequested)
			case "response":
				target = new(schema.ControlResponse)
			case "fault":
				target = new(schema.BoundaryFault)
			default:
				t.Fatal("unknown test envelope")
			}
			if err := schema.DecodeEnvelope(raw, target); (err == nil) != row.Valid {
				t.Fatalf("valid=%v, error=%v", row.Valid, err)
			}
		})
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
}
