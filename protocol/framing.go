// Package protocol defines Pitot's wire framing and request/response state
// machine helpers. Compatibility is defined by newline-delimited JSON framing
// and explicit state transitions, not by any client library.
package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// WriteLine encodes v as a single newline-delimited JSON record. Consumers read
// one JSON object per line from standard input.
func WriteLine(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("pitot: encode line: %w", err)
	}
	if bytes.ContainsRune(payload, '\n') {
		return fmt.Errorf("pitot: encoded record contains an embedded newline")
	}
	if _, err := w.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("pitot: write line: %w", err)
	}
	return nil
}

// DecodeLine decodes a single JSON-Lines record into v, rejecting trailing data
// so a malformed frame cannot smuggle a second object.
func DecodeLine(line []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(line)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("pitot: decode line: %w", err)
	}
	if decoder.More() {
		return fmt.Errorf("pitot: decode line: unexpected trailing content")
	}
	return nil
}

// NewReader returns a bufio.Scanner configured for JSON-Lines input with a
// generous line budget for large tool payloads.
func NewReader(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return scanner
}
