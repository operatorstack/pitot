// Command pitot_adapter_inventory prints the shipped Pitot adapter IDs for the
// repository supervisor. It is internal verification machinery, not a public
// Pitot command.
package main

import (
	"encoding/json"
	"os"

	"github.com/operatorstack/pitot/adapters"
	pitotruntime "github.com/operatorstack/pitot/runtime"
)

func main() {
	hosts := adapters.Supported()
	values := make([]string, len(hosts))
	for index, host := range hosts {
		values[index] = string(host)
	}
	capabilities := pitotruntime.Capabilities()
	capabilityValues := make([]string, len(capabilities))
	for index, capability := range capabilities {
		capabilityValues[index] = string(capability)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"adapters": values, "capabilities": capabilityValues}); err != nil {
		panic(err)
	}
}
