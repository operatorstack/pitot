// Package projection applies a Consumer's content policy before bytes cross the
// process boundary. Projection happens at the boundary, not inside downstream
// applications: a passive Consumer configured for sha256 or omit can never
// recover the raw payload.
package projection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/operatorstack/pitot/schema"
)

// Mode is a content projection policy.
type Mode string

// Supported projection modes.
const (
	Full   Mode = schema.ContentFull
	SHA256 Mode = schema.ContentSHA256
	Omit   Mode = schema.ContentOmit
)

// Valid reports whether m is a recognized projection mode.
func (m Mode) Valid() bool {
	switch m {
	case Full, SHA256, Omit:
		return true
	default:
		return false
	}
}

// Apply projects raw content according to mode. For Full the raw bytes are
// carried as a JSON string; for SHA256 only the digest is carried; for Omit no
// representation is carried at all.
func Apply(mode Mode, raw []byte) (schema.Content, error) {
	switch mode {
	case Full:
		encoded, err := json.Marshal(string(raw))
		if err != nil {
			return schema.Content{}, fmt.Errorf("pitot: project full content: %w", err)
		}
		return schema.Content{Mode: schema.ContentFull, Full: encoded}, nil
	case SHA256:
		digest := sha256.Sum256(raw)
		return schema.Content{Mode: schema.ContentSHA256, SHA256: hex.EncodeToString(digest[:])}, nil
	case Omit:
		return schema.Content{Mode: schema.ContentOmit}, nil
	default:
		return schema.Content{}, fmt.Errorf("pitot: unsupported projection mode %q", mode)
	}
}
