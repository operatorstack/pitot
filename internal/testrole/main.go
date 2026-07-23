// Command testrole is a language-neutral-process fixture for Pitot runtime tests.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/operatorstack/pitot/schema"
)

func main() {
	role := flag.String("role", "", "controller or consumer")
	id := flag.String("id", "test-controller", "controller identity")
	receipt := flag.String("receipt", "", "append-only receipt path")
	nonce := flag.String("nonce", "", "E2E nonce")
	mode := flag.String("mode", "route", "route, timeout, malformed, mismatch, allow, or deny")
	flag.Parse()
	if *role == "canary" {
		if err := canary(*receipt, flag.Args()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	if *role == "consumer" {
		copyLines(*receipt)
		return
	}
	if *role != "controller" {
		fmt.Fprintln(os.Stderr, "testrole: --role is required")
		os.Exit(2)
	}
	controller(*id, *receipt, *nonce, *mode)
}

func canary(receipt string, args []string) error {
	if receipt == "" || len(args) != 2 {
		return fmt.Errorf("testrole: canary requires --receipt, marker, and nonce")
	}
	appendLine(receipt, args[0]+" "+args[1])
	fmt.Printf("PITOT_CANARY_RESULT %s %s\n", args[0], args[1])
	return nil
}

func copyLines(receipt string) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		appendLine(receipt, scanner.Text())
	}
}

func controller(id, receipt, nonce, mode string) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request schema.ControlRequested
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		appendJSON(receipt, map[string]any{"receipt_type": "request", "value": request})
		switch mode {
		case "timeout":
			time.Sleep(10 * time.Second)
			continue
		case "malformed":
			fmt.Println(`{"not":"a control response"}`)
			continue
		}
		outcome := schema.OutcomeAllow
		if mode == "deny" || (mode == "route" && strings.Contains(string(request.Data), "PITOT_DENY")) {
			outcome = schema.OutcomeDeny
		}
		actionID := request.ActionID
		if mode == "mismatch" {
			actionID = "act_mismatch"
		}
		message := ""
		if outcome == schema.OutcomeDeny {
			message = "PITOT_CONTROLLER_DENY " + nonce
		}
		response := schema.ControlResponse{
			PitotVersion: schema.Version,
			Type:         schema.TypeControlResponse,
			ControllerID: id,
			ActionID:     actionID,
			Outcome:      outcome,
			Message:      message,
		}
		appendJSON(receipt, map[string]any{"receipt_type": "response", "value": response})
		_ = json.NewEncoder(os.Stdout).Encode(response)
	}
}

func appendJSON(path string, value any) {
	encoded, err := json.Marshal(value)
	if err == nil {
		appendLine(path, string(encoded))
	}
}

func appendLine(path, line string) {
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintln(file, line)
}
