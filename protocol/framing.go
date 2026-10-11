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
	record := append(payload, '\n')
	n, err := w.Write(record)
	if err != nil {
		return fmt.Errorf("pitot: write line: %w", err)
	}
	if n != len(record) {
		return fmt.Errorf("pitot: write line: %w", io.ErrShortWrite)
	}
	return nil
}

// DecodeLine decodes a single JSON-Lines record into v, rejecting trailing data
// so a malformed frame cannot smuggle a second object.
func DecodeLine(line []byte, v any) error {
	if _, err := Parse(line); err != nil {
		return ErrInvalidFrame
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(line)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("pitot: decode line: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("pitot: decode line: unexpected trailing content")
	}
	return nil
}

// ReadFrames invokes visit in order. Oversized lines are drained through their
// newline so a following valid record remains reachable. Empty lines are ignored.
func ReadFrames(r io.Reader, visit func([]byte, error)) error {
	reader := bufio.NewReader(r)
	var frame []byte
	oversized := false
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > 0 && part[len(part)-1] == '\n' {
			part = part[:len(part)-1]
		}
		if !oversized {
			if len(frame)+len(part) > MaxFrameBytes {
				oversized = true
				frame = nil
			} else {
				frame = append(frame, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil && err != io.EOF {
			return err
		}
		if oversized {
			visit(nil, ErrInvalidFrame)
		} else if len(bytes.Trim(frame, " \t\r\n")) != 0 {
			visit(frame, nil)
		}
		frame = nil
		oversized = false
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// NewReader returns a bufio.Scanner configured for JSON-Lines input with a
// generous line budget for large tool payloads.
func NewReader(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return scanner
}
