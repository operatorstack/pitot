// Command token-meter is a passive Consumer: it sums token usage from the Pitot
// event stream without ever recording prompts. It needs no Pitot SDK — it only
// reads newline-delimited JSON from standard input.
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
