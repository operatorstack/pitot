// Command token-meter sums application-supplied model.usage events without
// recording prompts. The shipped Pitot runtime delivers action.requested events,
// not model usage, so registering this Consumer does not collect token counts.
// This example reads one JSON record per line from standard input.
package main

import (
	"fmt"
	"os"

	"github.com/operatorstack/pitot/protocol"
	"github.com/operatorstack/pitot/schema"
)

func main() {
	total := 0
	scanner := protocol.NewReader(os.Stdin)
	for scanner.Scan() {
		var event schema.Event
		if err := protocol.DecodeLine(scanner.Bytes(), &event); err != nil {
			// A Consumer tolerates records it does not understand rather than
			// crashing the session.
			continue
		}
		if event.Type != schema.TypeModelUsage || event.Usage == nil {
			continue
		}
		total += event.Usage.InputTokens + event.Usage.OutputTokens
		fmt.Fprintf(os.Stderr, `{"session_tokens":%d}`+"\n", total)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
