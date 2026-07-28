// Upgrade is a reviewed data diff (upgrade-is-a-reviewed-data-diff): the only
// repository mutation this command makes is rewriting .pitot/version. The new
// binary lands in the write-once cache as a consequence of the committed pin,
// and every tenant fragment must still hold before the pin moves
// (upgrade-preserves-the-tenancy-contract).
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/hydrate"
	"github.com/operatorstack/pitot/schema"
	"go.yaml.in/yaml/v4"
)

func runUpgrade(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	target := ""
	check := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--to":
			if i+1 >= len(args) {
				return errors.New("pitot upgrade: --to requires a version")
			}
			target = args[i+1]
			i++
		case "--check":
			check = true
		case "--host":
			if i+1 >= len(args) {
				return errors.New("pitot upgrade: --host requires a value")
			}
			os.Setenv(hydrate.EnvHost, args[i+1])
			i++
		default:
			return fmt.Errorf("pitot upgrade: unexpected argument %q", args[i])
		}
	}

	current := "none"
	if pin, err := hydrate.Pin("."); err == nil {
		current = pin
	}
	if target == "" {
		latest, err := hydrate.Latest(ctx)
		if err != nil {
			return err
		}
		target = latest
	}
	target = strings.TrimPrefix(target, "v")

	if check {
		if current == target {
			fmt.Fprintf(stdout, "pinned %s — up to date\n", current)
		} else {
			fmt.Fprintf(stdout, "pinned %s, available %s (run: pitot upgrade)\n", current, target)
		}
		return nil
	}
	if current == target {
		fmt.Fprintf(stdout, "already pinned to %s\n", current)
		return nil
	}

	// The new binary must exist and verify before the pin moves.
	slot, err := hydrate.Ensure(ctx, target)
	if err != nil {
		return err
	}
	// Every tenant must still hold under the new binary before the pin moves.
	if err := validateTenantsFor(target, slot); err != nil {
		return err
	}
	if err := hydrate.WritePin(".", target); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "pinned %s -> %s (%s rewritten — commit this diff; every clone hydrates %s on its next invocation)\n", current, target, hydrate.PinPath, target)
	return nil
}

// validateTenantsFor re-checks the tenancy contract against the upgrade
// target: the merged config must still validate, and every fragment's
// requires_protocol must be spoken by the new binary.
func validateTenantsFor(version, slot string) error {
	if _, err := config.Discover("."); err != nil {
		if errors.Is(err, config.ErrNoConfig) {
			return nil // no tenants registered; nothing to preserve
		}
		return fmt.Errorf("pitot upgrade: refusing to move the pin while the tenant config is broken: %w (upgrade-preserves-the-tenancy-contract)", err)
	}
	supported, err := binaryProtocol(slot)
	if err != nil {
		return fmt.Errorf("pitot upgrade: could not determine the protocol of %s: %w", version, err)
	}
	floors, err := fragmentProtocolFloors(".")
	if err != nil {
		return err
	}
	for _, fragment := range sortedKeysOf(floors) {
		if floors[fragment] != supported {
			return fmt.Errorf("pitot upgrade: tenant %s requires protocol %q but pitot %s speaks protocol %q — refusing to move the pin (upgrade-preserves-the-tenancy-contract)", fragment, floors[fragment], version, supported)
		}
	}
	return nil
}

// binaryProtocol asks a hydrated binary which protocol it speaks. The current
// binary answers from memory; a different version is executed and parsed.
func binaryProtocol(slot string) (string, error) {
	if slot == "" {
		return schema.Version, nil
	}
	output, err := execCapture(slot, "version")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "protocol version") {
			fields := strings.Split(line, ":")
			return strings.TrimSpace(fields[len(fields)-1]), nil
		}
	}
	return "", fmt.Errorf("binary at %s did not report a protocol version", slot)
}

// fragmentProtocolFloors reads each tenant fragment's requires_protocol
// declaration (loose projection — full validation already ran via Discover).
func fragmentProtocolFloors(root string) (map[string]string, error) {
	dir := filepath.Join(root, ".pitot", "conf.d")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("pitot upgrade: read %s: %w", dir, err)
	}
	floors := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch filepath.Ext(entry.Name()) {
		case ".yaml", ".yml":
		default:
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var fragment struct {
			RequiresProtocol string `yaml:"requires_protocol"`
		}
		if err := yaml.Unmarshal(raw, &fragment); err != nil {
			continue // Discover already vouched for parseability under strict rules
		}
		if fragment.RequiresProtocol != "" {
			floors[filepath.ToSlash(filepath.Join(config.FragmentDir, entry.Name()))] = fragment.RequiresProtocol
		}
	}
	return floors, nil
}

func sortedKeysOf(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// execCapture runs a binary and returns its combined output.
func execCapture(binary string, args ...string) (string, error) {
	command := exec.Command(binary, args...)
	var buffer bytes.Buffer
	command.Stdout = &buffer
	command.Stderr = &buffer
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", binary, strings.Join(args, " "), err, buffer.String())
	}
	return buffer.String(), nil
}
