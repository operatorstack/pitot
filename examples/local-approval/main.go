// Command local-approval is a Controller: it receives a correlated
// control.requested record and returns exactly one control.response on standard
// output. It owns the definition of "approved"; Pitot only carries the answer.
package main

import (
	"fmt"
	"os"

	"github.com/operatorstack/pitot/protocol"
	"github.com/operatorstack/pitot/schema"
)

// controllerID must match the id registered for this request kind.
const controllerID = "local-approval"

func main() {
	scanner := protocol.NewReader(os.Stdin)
	if !scanner.Scan() {
		fmt.Fprintln(os.Stderr, "local-approval: no request on standard input")
		os.Exit(1)
	}
	var req schema.ControlRequested
	if err := protocol.DecodeLine(scanner.Bytes(), &req); err != nil {
		fmt.Fprintln(os.Stderr, "local-approval: malformed request:", err)
		os.Exit(1)
	}

	// A real Controller consults its own source of truth here. This reference
	// program denies by default and only allows an explicitly recognized action.
	outcome := schema.OutcomeDeny
	message := "no approval on record"
	if req.Kind == "release.approval" {
		outcome = schema.OutcomeAllow
		message = "release approved for publication"
	}

	resp := schema.ControlResponse{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlResponse,
		ControllerID: controllerID,
		ActionID:     req.ActionID,
		Outcome:      outcome,
		Message:      message,
	}
	if err := protocol.WriteLine(os.Stdout, resp); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
