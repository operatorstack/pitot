package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/protocol"
	"github.com/operatorstack/pitot/schema"
)

var benchmarkProjectedEvent schema.Event

func benchmarkEvent(size int) schema.Event {
	content, err := projection.Apply(projection.Full, []byte(strings.Repeat("a", size)))
	if err != nil {
		panic(err)
	}
	return schema.Event{
		PitotVersion: schema.Version, Type: schema.TypeActionRequested,
		Host: schema.Host{Name: "claude"}, Action: &schema.Action{ID: "benchmark", Kind: "shell"},
		Content: &content, Observation: schema.Observation{Source: schema.SourceHostHook, Fidelity: schema.FidelityDirect},
	}
}

func BenchmarkProjectEvent(b *testing.B) {
	for _, size := range []int{1024, 65536, 1048576} {
		for _, mode := range []projection.Mode{projection.Full, projection.SHA256, projection.Omit} {
			b.Run(fmt.Sprintf("bytes=%d/mode=%s", size, mode), func(b *testing.B) {
				event := benchmarkEvent(size)
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					projected, err := projectEvent(event, mode)
					if err != nil {
						b.Fatal(err)
					}
					benchmarkProjectedEvent = projected
				}
			})
		}
	}
}

// BenchmarkDeliverEvent includes validation, successful queue admission/draining
// and, in the controller cases, real child-process request/response I/O. Consumer
// application processing, HTTP transport and child allocations are not measured.
func BenchmarkDeliverEvent(b *testing.B) {
	b.Setenv("PITOT_BENCHMARK_CONTROLLER", "1")
	for _, controlled := range []bool{false, true} {
		for _, size := range []int{1024, 65536, 1048576} {
			for _, mode := range []projection.Mode{projection.Full, projection.SHA256, projection.Omit} {
				for _, fanout := range []int{1, 4, 16} {
					b.Run(fmt.Sprintf("controller=%t/bytes=%d/mode=%s/consumers=%d", controlled, size, mode, fanout), func(b *testing.B) {
						event := benchmarkEvent(size)
						modes := make([]projection.Mode, fanout)
						for i := range modes {
							modes[i] = mode
						}
						manager := queuedManager(modes...)
						if controlled {
							actual, err := Start(context.Background(), config.Config{Controllers: map[string]config.ControllerConfig{"shell": {
								ID: "benchmark", Command: []string{os.Args[0], "-test.run=^TestBenchmarkController$"},
								DeadlineMS: 5000, OnTimeout: schema.OutcomeDeny, OnUnavailable: schema.OutcomeDeny,
							}}}, io.Discard)
							if err != nil {
								b.Fatal(err)
							}
							actual.consumers = manager.consumers
							manager = actual
							b.Cleanup(func() {
								manager.Close()
								select {
								case <-manager.controllers["shell"].done:
								case <-time.After(5 * time.Second):
									b.Error("benchmark controller did not exit")
								}
							})
						}
						b.ReportAllocs()
						b.SetBytes(int64(size))
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							action := *event.Action
							action.ID = "benchmark-" + strconv.Itoa(i)
							event.Action = &action
							response, err := manager.DeliverEvent(context.Background(), event)
							if err != nil || (controlled && (response == nil || response.Outcome != schema.OutcomeAllow)) || (!controlled && response != nil) {
								b.Fatalf("delivery = %+v, %v", response, err)
							}
							for _, consumer := range manager.consumers {
								<-consumer.queue
							}
						}
					})
				}
			}
		}
	}
}

// TestBenchmarkController is an owned subprocess used only by the benchmark.
func TestBenchmarkController(t *testing.T) {
	if os.Getenv("PITOT_BENCHMARK_CONTROLLER") != "1" {
		return
	}
	reader := protocol.NewReader(os.Stdin)
	for reader.Scan() {
		var request schema.ControlRequested
		if err := json.Unmarshal(reader.Bytes(), &request); err != nil || schema.ValidateRequest(request) != nil {
			os.Exit(2)
		}
		response := schema.ControlResponse{PitotVersion: schema.Version, Type: schema.TypeControlResponse,
			ControllerID: "benchmark", ActionID: request.ActionID, Outcome: schema.OutcomeAllow}
		if err := protocol.WriteLine(os.Stdout, response); err != nil {
			os.Exit(3)
		}
	}
	if reader.Err() != nil {
		os.Exit(4)
	}
	os.Exit(0)
}
