package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/invopop/jsonschema"
	"github.com/operatorstack/pitot/schema"
)

// PitotContract acts as a root object to force generation of all these types.
type PitotContract struct {
	Event            schema.Event            `json:"event"`
	ControlRequested schema.ControlRequested `json:"control_requested"`
	ControlResponse  schema.ControlResponse  `json:"control_response"`
	BoundaryFault    schema.BoundaryFault    `json:"boundary_fault"`
}

func main() {
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
	}

	sch := reflector.Reflect(&PitotContract{})

	b, err := json.MarshalIndent(sch, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to marshal schema: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(b))
}
