// Package conformance holds Pitot's language-neutral fixture corpus and the
// loader that drives it. The same positive and negative vectors serve three
// roles: a decoder regression baseline, the contract every host adapter must
// satisfy, and evidence for the release gate. The corpus is deliberately data,
// not code, so a non-Go adapter can consume the identical fixtures.
package conformance

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed fixtures/*.jsonl
var fixtures embed.FS

// Case is one conformance vector. Exactly one of Input (a structured host
// payload) or InputRaw (a verbatim byte string, used for malformed controls) is
// populated.
type Case struct {
	Name       string          `json:"name"`
	Host       string          `json:"host"`
	Mode       string          `json:"mode"`
	Input      json.RawMessage `json:"input,omitempty"`
	InputRaw   *string         `json:"input_raw,omitempty"`
	ExpectKind string          `json:"expect_kind,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

// Payload returns the raw host payload bytes the sensor should decode.
func (c Case) Payload() []byte {
	if c.InputRaw != nil {
		return []byte(*c.InputRaw)
	}
	return c.Input
}

// Positive returns the vectors that must decode into a normalized event.
func Positive() ([]Case, error) { return load("fixtures/positive.jsonl") }

// Negative returns the vectors that must raise a content-safe boundary fault.
func Negative() ([]Case, error) { return load("fixtures/negative.jsonl") }

func load(path string) ([]Case, error) {
	data, err := fixtures.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("conformance: read %s: %w", path, err)
	}
	var cases []Case
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var c Case
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("conformance: %s line %d: %w", path, line, err)
		}
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("conformance: scan %s: %w", path, err)
	}
	return cases, nil
}
