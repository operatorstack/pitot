package protocol_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/operatorstack/pitot/protocol"
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
	if err := protocol.WriteLine(&buf, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Fatal("record must end with a newline")
	}
	var got schema.ControlResponse
	if err := protocol.DecodeLine(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("round trip mismatch: got %+v want %+v", got, want)
	}
}

func TestDecodeRejectsTrailingContent(t *testing.T) {
	for _, raw := range []string{`{"pitot_version":"1"}{"x":1}`, `{"pitot_version":"1"}}`, `{"pitot_version":"1"}]`} {
		var got schema.ControlResponse
		if err := protocol.DecodeLine([]byte(raw), &got); err == nil {
			t.Fatal("expected trailing content to be rejected")
		}
	}
}

func TestReadFramesDrainsOversizedRecord(t *testing.T) {
	input := strings.Repeat("x", protocol.MaxFrameBytes+1) + "\n \r\n{}\r\n{}"
	var values []string
	rejected := 0
	err := protocol.ReadFrames(strings.NewReader(input), func(raw []byte, err error) {
		if err != nil {
			rejected++
			return
		}
		values = append(values, string(raw))
	})
	if err != nil || rejected != 1 || len(values) != 2 || values[0] != "{}\r" || values[1] != "{}" {
		t.Fatalf("err=%v, rejected=%d, values=%v", err, rejected, values)
	}
}

type shortFrameWriter struct{}

func (shortFrameWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestWriteLineRejectsShortWrite(t *testing.T) {
	if err := protocol.WriteLine(shortFrameWriter{}, map[string]string{"record": "value"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error=%v", err)
	}
}
