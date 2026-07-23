// Package config defines and validates Pitot's repository-owned runtime configuration.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/schema"
	"go.yaml.in/yaml/v4"
)

// Config is the complete v1 process-delivery boundary.
type Config struct {
	Consumers   []ConsumerConfig            `yaml:"consumers,omitempty"`
	Controllers map[string]ControllerConfig `yaml:"controllers,omitempty"`
}

// ConsumerConfig declares a passive JSON-Lines event sink.
type ConsumerConfig struct {
	ID         string           `yaml:"id"`
	Command    []string         `yaml:"command"`
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
	DeadlineMS    int      `yaml:"deadline_ms"`
	OnTimeout     string   `yaml:"on_timeout"`
	OnUnavailable string   `yaml:"on_unavailable"`
}

// Loaded preserves the validated config and the digest bound into its runtime descriptor.
type Loaded struct {
	Config Config
	SHA256 string
}

// Load reads exactly one strict YAML document and validates its complete process surface.
func Load(path string) (Loaded, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Loaded{}, fmt.Errorf("pitot: read config %q: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Loaded{}, fmt.Errorf("pitot: decode config %q: %w", path, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Loaded{}, fmt.Errorf("pitot: config %q must contain exactly one YAML document", path)
		}
		return Loaded{}, fmt.Errorf("pitot: decode trailing config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Loaded{}, err
	}
	digest := sha256.Sum256(raw)
	return Loaded{Config: cfg, SHA256: hex.EncodeToString(digest[:])}, nil
}

// Validate enforces role separation and deterministic registration.
func (c Config) Validate() error {
	if len(c.Consumers) == 0 && len(c.Controllers) == 0 {
		return errors.New("pitot: config requires at least one consumer or controller")
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
