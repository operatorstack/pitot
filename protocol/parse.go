package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

const MaxFrameBytes = 4 * 1024 * 1024
const MaxDepth = 128

var ErrInvalidFrame = errors.New("pitot: invalid JSON frame")

// Parse validates the transport grammar before any typed decoder can discard
// duplicate names or replace invalid Unicode. Numbers retain their spelling.
func Parse(line []byte) (any, error) {
	if len(line) > MaxFrameBytes || !utf8.Valid(line) || !scalarEscapes(line) {
		return nil, ErrInvalidFrame
	}
	d := json.NewDecoder(bytes.NewReader(line))
	d.UseNumber()
	v, err := value(d, 0)
	if err != nil {
		return nil, ErrInvalidFrame
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrInvalidFrame
	}
	return v, nil
}

func value(d *json.Decoder, depth int) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	start, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	if depth >= MaxDepth {
		return nil, ErrInvalidFrame
	}
	switch start {
	case '{':
		m := make(map[string]any)
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := k.(string)
			if !ok {
				return nil, ErrInvalidFrame
			}
			if _, exists := m[name]; exists {
				return nil, ErrInvalidFrame
			}
			v, err := value(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[name] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrInvalidFrame
		}
		return m, nil
	case '[':
		var a []any
		for d.More() {
			v, err := value(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrInvalidFrame
		}
		return a, nil
	default:
		return nil, ErrInvalidFrame
	}
}

// encoding/json replaces unpaired escaped surrogates. Check those escapes
// before decoding; all other string grammar is still checked by the decoder.
func scalarEscapes(b []byte) bool {
	inString := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		n, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xDC00 && n <= 0xDFFF {
			return false
		}
		if n >= 0xD800 && n <= 0xDBFF {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}
