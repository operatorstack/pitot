// Package config defines and validates Pitot's repository-owned runtime
// configuration. Configuration is tenant-partitioned: each tool or user
// registers its processes in its own fragment under .pitot/conf.d/, and the
// effective config is the deterministic merge of every fragment. No tenant
// ever edits another tenant's registration.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
	"go.yaml.in/yaml/v4"
)

// FragmentDir is the repository-relative directory holding one config
// fragment per tenant.
const FragmentDir = ".pitot/conf.d"

// ErrNoConfig reports that discovery found no config fragments.
var ErrNoConfig = errors.New("pitot: no config fragments found under " + FragmentDir)

// Config is one tenant's complete process registration — and, once merged,
// the complete v1 process-delivery boundary.
type Config struct {
	// RequiresProtocol optionally pins the Pitot protocol version a fragment
	// was written against. A fragment demanding a protocol this binary does
	// not speak fails discovery instead of misbehaving at runtime.
	RequiresProtocol string                      `yaml:"requires_protocol,omitempty"`
	Consumers        []ConsumerConfig            `yaml:"consumers,omitempty"`
	Controllers      map[string]ControllerConfig `yaml:"controllers,omitempty"`
	// RequireController converts silent observation-only degradation into a
	// fault: an action kind delivered without a registered Controller is
	// blocked instead of allowed. Additive and off by default — plain
	// observation-only operation is unchanged. Any fragment declaring it
	// makes the merged config strict.
	RequireController bool `yaml:"require_controller,omitempty"`
}

// ConsumerConfig declares a passive JSON-Lines event sink.
type ConsumerConfig struct {
	ID         string           `yaml:"id"`
	Command    []string         `yaml:"command"`
	Dir        string           `yaml:"dir,omitempty"`
	Events     []string         `yaml:"events"`
	Projection ProjectionConfig `yaml:"projection"`
}

// ProjectionConfig controls what event content crosses a Consumer's pipe.
type ProjectionConfig struct {
	Content projection.Mode `yaml:"content"`
}

// ControllerConfig declares one synchronous decision process for a request kind.
type ControllerConfig struct {
	ID            string   `yaml:"id"`
	Command       []string `yaml:"command"`
	Dir           string   `yaml:"dir,omitempty"`
	DeadlineMS    int      `yaml:"deadline_ms"`
	OnTimeout     string   `yaml:"on_timeout"`
	OnUnavailable string   `yaml:"on_unavailable"`
}

// Loaded preserves the validated config and the digest bound into its runtime descriptor.
type Loaded struct {
	Config Config
	SHA256 string
}

// Load reads exactly one strict YAML document from an explicit path and
// validates its complete process surface. This is the single-file override
// path (`pitot run --config PATH`); repository configs are discovered from
// fragments via Discover.
func Load(path string) (Loaded, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Loaded{}, fmt.Errorf("pitot: read config %q: %w", path, err)
	}
	cfg, err := decodeStrict(raw)
	if err != nil {
		return Loaded{}, fmt.Errorf("pitot: config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Loaded{}, err
	}
	digest := sha256.Sum256(raw)
	return Loaded{Config: cfg, SHA256: hex.EncodeToString(digest[:])}, nil
}

// FindRoot resolves the Pitot repository root from a starting directory by
// walking UP to the nearest directory owning .pitot/conf.d. The walk stops
// at the first .git boundary, at the user's home directory, and at the
// volume root — and it NEVER descends: a dependency's .pitot (e.g. inside
// node_modules) is invisible from outside it
// (discovery-walks-up-to-owned-roots-only).
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	for {
		if info, statErr := os.Stat(filepath.Join(dir, ".pitot", "conf.d")); statErr == nil && info.IsDir() {
			return dir, nil
		}
		if _, statErr := os.Lstat(filepath.Join(dir, ".git")); statErr == nil {
			return "", ErrNoConfig // owned boundary reached without a Pitot substrate
		}
		parent := filepath.Dir(dir)
		if dir == home || parent == dir {
			return "", ErrNoConfig
		}
		dir = parent
	}
}

// source is one fragment's filename and raw bytes.
type source struct {
	name string
	raw  []byte
}

// Discover assembles the effective config from every fragment under
// root/.pitot/conf.d, in lexicographic filename order. Each fragment is a
// strict, complete mini-config owned by one tenant. Collisions across
// fragments — a request kind, controller id, or consumer id claimed twice —
// are hard errors naming both source files. The digest is deterministic over
// the sorted fragment set.
func Discover(root string) (Loaded, error) {
	sources, err := readSources(root)
	if err != nil {
		return Loaded{}, err
	}
	if len(sources) == 0 {
		return Loaded{}, ErrNoConfig
	}
	return mergeSources(sources)
}

// Preflight reports whether registering a new fragment (name + raw bytes)
// would merge cleanly with the fragments already present under root. A
// same-named existing fragment is treated as replaced by the candidate.
func Preflight(root, name string, raw []byte) error {
	sources, err := readSources(root)
	if err != nil {
		return err
	}
	kept := sources[:0]
	for _, existing := range sources {
		if existing.name != name {
			kept = append(kept, existing)
		}
	}
	_, err = mergeSources(append(kept, source{name: name, raw: raw}))
	return err
}

// readSources collects the fragment files under root/.pitot/conf.d in sorted
// filename order. A missing directory is an empty, not an error.
func readSources(root string) ([]source, error) {
	dir := filepath.Join(root, ".pitot", "conf.d")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("pitot: read config directory %q: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch filepath.Ext(entry.Name()) {
		case ".yaml", ".yml":
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	sources := make([]source, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("pitot: read fragment %q: %w", filepath.ToSlash(filepath.Join(FragmentDir, name)), err)
		}
		sources = append(sources, source{name: name, raw: raw})
	}
	return sources, nil
}

// mergeSources unions fragments into the effective config, enforcing the
// tenancy contract: strict per-fragment decode, one owner per request kind,
// globally unique controller and consumer ids, and a digest that is a pure
// function of the sorted fragment set.
func mergeSources(sources []source) (Loaded, error) {
	sort.Slice(sources, func(i, j int) bool { return sources[i].name < sources[j].name })
	merged := Config{Controllers: map[string]ControllerConfig{}}
	kindSource := map[string]string{}
	controllerIDSource := map[string]string{}
	consumerIDSource := map[string]string{}
	var manifest bytes.Buffer
	for _, item := range sources {
		rel := filepath.ToSlash(filepath.Join(FragmentDir, item.name))
		fragment, err := decodeStrict(item.raw)
		if err != nil {
			return Loaded{}, fmt.Errorf("pitot: fragment %q: %w", rel, err)
		}
		if len(fragment.Consumers) == 0 && len(fragment.Controllers) == 0 {
			return Loaded{}, fmt.Errorf("pitot: fragment %q declares no consumers or controllers", rel)
		}
		if fragment.RequireController {
			merged.RequireController = true
		}
		if fragment.RequiresProtocol != "" && fragment.RequiresProtocol != schema.Version {
			return Loaded{}, fmt.Errorf("pitot: fragment %q requires protocol %q but this pitot speaks protocol %q", rel, fragment.RequiresProtocol, schema.Version)
		}
		for _, consumer := range fragment.Consumers {
			if other, exists := consumerIDSource[consumer.ID]; exists {
				return Loaded{}, fmt.Errorf("pitot: consumer id %q is declared by both %s and %s", consumer.ID, other, rel)
			}
			consumerIDSource[consumer.ID] = rel
			merged.Consumers = append(merged.Consumers, consumer)
		}
		for _, kind := range sortedControllerKinds(fragment.Controllers) {
			controller := fragment.Controllers[kind]
			if other, exists := kindSource[kind]; exists {
				return Loaded{}, fmt.Errorf("pitot: request kind %q is claimed by both %s and %s", kind, other, rel)
			}
			kindSource[kind] = rel
			if other, exists := controllerIDSource[controller.ID]; exists {
				return Loaded{}, fmt.Errorf("pitot: controller id %q is declared by both %s and %s", controller.ID, other, rel)
			}
			controllerIDSource[controller.ID] = rel
			merged.Controllers[kind] = controller
		}
		digest := sha256.Sum256(item.raw)
		fmt.Fprintf(&manifest, "%s %s\n", item.name, hex.EncodeToString(digest[:]))
	}
	if err := merged.Validate(); err != nil {
		return Loaded{}, err
	}
	digest := sha256.Sum256(manifest.Bytes())
	return Loaded{Config: merged, SHA256: hex.EncodeToString(digest[:])}, nil
}

// decodeStrict decodes exactly one YAML document with unknown fields rejected.
func decodeStrict(raw []byte) (Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("must contain exactly one YAML document")
		}
		return Config{}, fmt.Errorf("decode trailing document: %w", err)
	}
	return cfg, nil
}

// Validate enforces role separation and deterministic registration.
func (c Config) Validate() error {
	if len(c.Consumers) == 0 && len(c.Controllers) == 0 {
		return errors.New("pitot: config requires at least one consumer or controller")
	}
	if c.RequiresProtocol != "" && c.RequiresProtocol != schema.Version {
		return fmt.Errorf("pitot: config requires protocol %q but this pitot speaks protocol %q", c.RequiresProtocol, schema.Version)
	}
	consumerIDs := map[string]struct{}{}
	for i, consumer := range c.Consumers {
		if consumer.ID == "" {
			return fmt.Errorf("pitot: consumer %d requires an id", i)
		}
		if _, exists := consumerIDs[consumer.ID]; exists {
			return fmt.Errorf("pitot: duplicate consumer id %q", consumer.ID)
		}
		consumerIDs[consumer.ID] = struct{}{}
		if err := validateCommand("consumer "+consumer.ID, consumer.Command); err != nil {
			return err
		}
		if err := validateDir("consumer "+consumer.ID, consumer.Dir); err != nil {
			return err
		}
		if len(consumer.Events) == 0 {
			return fmt.Errorf("pitot: consumer %q requires at least one event", consumer.ID)
		}
		seenEvents := map[string]struct{}{}
		for _, event := range consumer.Events {
			if event != schema.TypeActionRequested {
				return fmt.Errorf("pitot: consumer %q has unsupported event %q", consumer.ID, event)
			}
			if _, exists := seenEvents[event]; exists {
				return fmt.Errorf("pitot: consumer %q repeats event %q", consumer.ID, event)
			}
			seenEvents[event] = struct{}{}
		}
		if !consumer.Projection.Content.Valid() {
			return fmt.Errorf("pitot: consumer %q has invalid content projection %q", consumer.ID, consumer.Projection.Content)
		}
	}
	controllerIDs := map[string]string{}
	for _, kind := range sortedControllerKinds(c.Controllers) {
		controller := c.Controllers[kind]
		if kind == "" {
			return errors.New("pitot: controller request kind cannot be empty")
		}
		if controller.ID == "" {
			return fmt.Errorf("pitot: controller for %q requires an id", kind)
		}
		if other, exists := controllerIDs[controller.ID]; exists {
			return fmt.Errorf("pitot: controller id %q is registered for both %q and %q", controller.ID, other, kind)
		}
		controllerIDs[controller.ID] = kind
		if err := validateCommand("controller "+controller.ID, controller.Command); err != nil {
			return err
		}
		if err := validateDir("controller "+controller.ID, controller.Dir); err != nil {
			return err
		}
		if controller.DeadlineMS <= 0 {
			return fmt.Errorf("pitot: controller %q requires a positive deadline_ms", controller.ID)
		}
		if !validOutcome(controller.OnTimeout) {
			return fmt.Errorf("pitot: controller %q has invalid on_timeout %q", controller.ID, controller.OnTimeout)
		}
		if !validOutcome(controller.OnUnavailable) {
			return fmt.Errorf("pitot: controller %q has invalid on_unavailable %q", controller.ID, controller.OnUnavailable)
		}
	}
	return nil
}

func validateCommand(role string, command []string) error {
	if len(command) == 0 || command[0] == "" {
		return fmt.Errorf("pitot: %s requires a command", role)
	}
	for _, argument := range command {
		if argument == "" {
			return fmt.Errorf("pitot: %s command contains an empty argument", role)
		}
	}
	return nil
}

// validateDir keeps process working directories repository-relative so a
// committed fragment behaves identically on every checkout.
func validateDir(role, dir string) error {
	if dir == "" {
		return nil
	}
	if filepath.IsAbs(dir) {
		return fmt.Errorf("pitot: %s dir %q must be relative to the repository root", role, dir)
	}
	return nil
}

func validOutcome(value string) bool {
	return value == schema.OutcomeAllow || value == schema.OutcomeDeny
}

func sortedControllerKinds(values map[string]ControllerConfig) []string {
	kinds := make([]string, 0, len(values))
	for kind := range values {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}
