package sdk

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/operatorstack/pitot/schema"
)

type ConsumerHandler func(event schema.Event)

func RunConsumer(handler ConsumerHandler) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal(line, &event); err != nil {
			fmt.Fprintf(os.Stderr, "Pitot Consumer error: %v\n", err)
			continue
		}
		handler(event)
	}
}

type Outcome struct {
	Outcome string
	Message string
}

func Allow(message string) Outcome {
	return Outcome{Outcome: schema.OutcomeAllow, Message: message}
}

func Deny(message string) Outcome {
	return Outcome{Outcome: schema.OutcomeDeny, Message: message}
}

type ControllerHandler func(req schema.ControlRequested) Outcome

func RunController(controllerID string, handler ControllerHandler) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req schema.ControlRequested
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintf(os.Stderr, "Pitot Controller error: %v\n", err)
			continue
		}
		
		result := handler(req)
		
		resp := schema.ControlResponse{
			PitotVersion: schema.Version,
			Type:         schema.TypeControlResponse,
			ControllerID: controllerID,
			ActionID:     req.ActionID,
			Outcome:      result.Outcome,
			Message:      result.Message,
		}
		
		if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
			fmt.Fprintf(os.Stderr, "Pitot Controller error: %v\n", err)
		}
	}
}
