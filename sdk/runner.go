package sdk

import (
	"encoding/json"
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/operatorstack/pitot/protocol"
	"github.com/operatorstack/pitot/schema"
)

type ConsumerHandler func(event schema.Event)

func RunConsumer(handler ConsumerHandler) {
	err := protocol.ReadFrames(os.Stdin, func(line []byte, frameErr error) {
		var event schema.Event
		if frameErr != nil || schema.DecodeEnvelope(line, &event) != nil {
			fmt.Fprintln(os.Stderr, "Pitot Consumer error: invalid envelope")
			return
		}
		handler(event)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Pitot Consumer error: input unavailable")
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
	err := protocol.ReadFrames(os.Stdin, func(line []byte, frameErr error) {
		var req schema.ControlRequested
		if frameErr != nil || schema.DecodeEnvelope(line, &req) != nil {
			fmt.Fprintln(os.Stderr, "Pitot Controller error: invalid envelope")
			return
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
		if schema.ValidateResponse(resp) != nil || !utf8.ValidString(resp.Message) || !utf8.ValidString(controllerID) {
			fmt.Fprintln(os.Stderr, "Pitot Controller error: invalid response")
			return
		}

		encoded, err := json.Marshal(resp)
		if err != nil || len(encoded) > protocol.MaxFrameBytes {
			fmt.Fprintln(os.Stderr, "Pitot Controller error: invalid response")
			return
		}
		if _, err := os.Stdout.Write(append(encoded, '\n')); err != nil {
			fmt.Fprintln(os.Stderr, "Pitot Controller error: output unavailable")
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Pitot Controller error: input unavailable")
	}
}
