package protocol

import (
	"bytes"
	"testing"

	"github.com/operatorstack/pitot/schema"
)

func TestWriteThenDecodeRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	want := schema.ControlResponse{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlResponse,
		ControllerID: "local-approval",
		ActionID:     "act_7f2",
		Outcome:      schema.OutcomeAllow,
	}
	if err := WriteLine(&buf, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Fatal("record must end with a newline")
	}
	var got schema.ControlResponse
	if err := DecodeLine(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("round trip mismatch: got %+v want %+v", got, want)
	}
}

func TestDecodeRejectsTrailingContent(t *testing.T) {
	var got schema.ControlResponse
	if err := DecodeLine([]byte(`{"pitot_version":"1"}{"x":1}`), &got); err == nil {
		t.Fatal("expected trailing content to be rejected")
	}
}
