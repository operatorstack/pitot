// Package runtime owns Pitot's local child-process delivery boundary.
// It transports observations and decisions but contains no policy engine.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/operatorstack/pitot/bridge"
	"github.com/operatorstack/pitot/config"
	"github.com/operatorstack/pitot/projection"
	"github.com/operatorstack/pitot/protocol"
	"github.com/operatorstack/pitot/schema"
)

const consumerQueueSize = 128

// Manager starts configured role processes and exposes their shared delivery path.
type Manager struct {
	ctx         context.Context
	cancel      context.CancelFunc
	stderr      io.Writer
	controllers map[string]*controllerWorker
	consumers   []*consumerWorker
}

// Start creates the complete configured process boundary. A child that cannot
// start remains unavailable so the declared default can resolve requests.
func Start(parent context.Context, cfg config.Config, stderr io.Writer) (*Manager, error) {
	ctx, cancel := context.WithCancel(parent)
	manager := &Manager{
		ctx:         ctx,
		cancel:      cancel,
		stderr:      stderr,
		controllers: map[string]*controllerWorker{},
	}
	for _, kind := range sortedKinds(cfg.Controllers) {
		declared := cfg.Controllers[kind]
		registration := bridge.Registration{
			Kind:          kind,
			ControllerID:  declared.ID,
			DeadlineMS:    declared.DeadlineMS,
			OnTimeout:     declared.OnTimeout,
			OnUnavailable: declared.OnUnavailable,
		}
		router := bridge.NewRouter()
		if err := router.Register(registration); err != nil {
			cancel()
			return nil, err
		}
		worker := newControllerWorker(ctx, router, registration, declared.Command, stderr)
		manager.controllers[kind] = worker
		if worker.startErr != nil {
			fmt.Fprintf(stderr, "pitot: controller %q unavailable: %v\n", declared.ID, worker.startErr)
		}
	}
	for _, declared := range cfg.Consumers {
		worker := newConsumerWorker(ctx, declared, stderr)
		manager.consumers = append(manager.consumers, worker)
		if worker.startErr != nil {
			fmt.Fprintf(stderr, "pitot: consumer %q unavailable: %v\n", declared.ID, worker.startErr)
		}
	}
	return manager, nil
}

// Close stops every child process owned by the runtime.
func (m *Manager) Close() {
	m.cancel()
	for _, consumer := range m.consumers {
		consumer.close()
	}
	for _, controller := range m.controllers {
		controller.close()
	}
}

// DeliverEvent fans an observation to Consumers and, when registered, resolves
// the synchronous action through its Controller. A nil response means the
// action kind is observation-only.
func (m *Manager) DeliverEvent(ctx context.Context, event schema.Event) (*schema.ControlResponse, error) {
	for _, consumer := range m.consumers {
		if err := consumer.offer(event); err != nil {
			fmt.Fprintf(m.stderr, "pitot: consumer %q delivery fault: %v\n", consumer.id, err)
		}
	}
	if event.Action == nil {
		return nil, errors.New("pitot: controllable event requires an action")
	}
	worker, exists := m.controllers[event.Action.Kind]
	if !exists {
		return nil, nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("pitot: encode controller event: %w", err)
	}
	response, resolveErr := worker.resolve(ctx, schema.ControlRequested{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlRequested,
		Kind:         event.Action.Kind,
		ActionID:     event.Action.ID,
		Data:         data,
	})
	return &response, resolveErr
}

// Request routes an explicit request through the same Controller worker used by hooks.
func (m *Manager) Request(ctx context.Context, request schema.ControlRequested) (schema.ControlResponse, error) {
	worker, exists := m.controllers[request.Kind]
	if !exists {
		return schema.ControlResponse{}, bridge.ErrNoController
	}
	return worker.resolve(ctx, request)
}

type controllerResult struct {
	response schema.ControlResponse
	err      error
}

type controllerWorker struct {
	ctx          context.Context
	router       *bridge.Router
	registration bridge.Registration
	stdin        io.WriteCloser
	responses    chan controllerResult
	done         chan error
	startErr     error
	mu           sync.Mutex
	resolved     map[string]struct{}
	resolvedFIFO []string
	closeOnce    sync.Once
}

func newControllerWorker(ctx context.Context, router *bridge.Router, registration bridge.Registration, command []string, stderr io.Writer) *controllerWorker {
	worker := &controllerWorker{
		ctx:          ctx,
		router:       router,
		registration: registration,
		responses:    make(chan controllerResult, 16),
		done:         make(chan error, 1),
		resolved:     map[string]struct{}{},
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		worker.startErr = err
		return worker
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		worker.startErr = err
		return worker
	}
	if err := cmd.Start(); err != nil {
		worker.startErr = err
		return worker
	}
	worker.stdin = stdin
	go worker.readResponses(stdout)
	go func() { worker.done <- cmd.Wait() }()
	return worker
}

func (w *controllerWorker) readResponses(reader io.Reader) {
	scanner := protocol.NewReader(reader)
	for scanner.Scan() {
		var response schema.ControlResponse
		if err := protocol.DecodeLine(scanner.Bytes(), &response); err != nil {
			w.responses <- controllerResult{err: err}
			return
		}
		select {
		case w.responses <- controllerResult{response: response}:
		case <-w.ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case w.responses <- controllerResult{err: err}:
		case <-w.ctx.Done():
		}
	}
}

func (w *controllerWorker) resolve(ctx context.Context, request schema.ControlRequested) (schema.ControlResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.startErr != nil || w.stdin == nil {
		response, err := w.router.Resolve(request, nil)
		return response, errors.Join(w.startErr, err)
	}
	if err := protocol.WriteLine(w.stdin, request); err != nil {
		w.startErr = err
		response, resolveErr := w.router.Resolve(request, nil)
		return response, errors.Join(err, resolveErr)
	}
	deadline := time.NewTimer(time.Duration(w.registration.DeadlineMS) * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case result := <-w.responses:
			if result.err != nil {
				w.startErr = result.err
				response, resolveErr := w.router.Resolve(request, nil)
				w.remember(request.ActionID)
				return response, errors.Join(result.err, resolveErr)
			}
			if result.response.ActionID != request.ActionID {
				if _, stale := w.resolved[result.response.ActionID]; stale {
					continue
				}
				response, err := w.router.Resolve(request, &result.response)
				w.remember(request.ActionID)
				return response, err
			}
			response, err := w.router.Resolve(request, &result.response)
			w.remember(request.ActionID)
			return response, err
		case err := <-w.done:
			if err == nil {
				err = errors.New("controller exited")
			}
			w.startErr = err
			response, resolveErr := w.router.Resolve(request, nil)
			w.remember(request.ActionID)
			return response, errors.Join(err, resolveErr)
		case <-deadline.C:
			response, err := w.router.TimeoutResponse(request)
			w.remember(request.ActionID)
			return response, err
		case <-ctx.Done():
			response, err := w.router.TimeoutResponse(request)
			w.remember(request.ActionID)
			return response, errors.Join(ctx.Err(), err)
		case <-w.ctx.Done():
			response, err := w.router.Resolve(request, nil)
			w.remember(request.ActionID)
			return response, errors.Join(w.ctx.Err(), err)
		}
	}
}

func (w *controllerWorker) remember(actionID string) {
	w.resolved[actionID] = struct{}{}
	w.resolvedFIFO = append(w.resolvedFIFO, actionID)
	if len(w.resolvedFIFO) > 1024 {
		delete(w.resolved, w.resolvedFIFO[0])
		w.resolvedFIFO = w.resolvedFIFO[1:]
	}
}

func (w *controllerWorker) close() {
	w.closeOnce.Do(func() {
		if w.stdin != nil {
			_ = w.stdin.Close()
		}
	})
}

type consumerWorker struct {
	id         string
	events     map[string]struct{}
	projection projection.Mode
	queue      chan schema.Event
	stdin      io.WriteCloser
	startErr   error
	dead       chan struct{}
	closeOnce  sync.Once
}

func newConsumerWorker(ctx context.Context, declared config.ConsumerConfig, stderr io.Writer) *consumerWorker {
	worker := &consumerWorker{
		id:         declared.ID,
		events:     map[string]struct{}{},
		projection: declared.Projection.Content,
		queue:      make(chan schema.Event, consumerQueueSize),
		dead:       make(chan struct{}),
	}
	for _, event := range declared.Events {
		worker.events[event] = struct{}{}
	}
	cmd := exec.CommandContext(ctx, declared.Command[0], declared.Command[1:]...)
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		worker.startErr = err
		close(worker.dead)
		return worker
	}
	if err := cmd.Start(); err != nil {
		worker.startErr = err
		close(worker.dead)
		return worker
	}
	worker.stdin = stdin
	go worker.deliver(ctx)
	go func() {
		_ = cmd.Wait()
		worker.closeOnce.Do(func() { close(worker.dead) })
	}()
	return worker
}

func (w *consumerWorker) offer(event schema.Event) error {
	if _, subscribed := w.events[event.Type]; !subscribed {
		return nil
	}
	projected, err := projectEvent(event, w.projection)
	if err != nil {
		return err
	}
	select {
	case <-w.dead:
		return errors.New("consumer process is unavailable")
	case w.queue <- projected:
		return nil
	default:
		return errors.New("consumer queue is full")
	}
}

func (w *consumerWorker) deliver(ctx context.Context) {
	for {
		select {
		case event := <-w.queue:
			if err := protocol.WriteLine(w.stdin, event); err != nil {
				w.closeOnce.Do(func() { close(w.dead) })
				return
			}
		case <-ctx.Done():
			return
		case <-w.dead:
			return
		}
	}
}

func (w *consumerWorker) close() {
	if w.stdin != nil {
		_ = w.stdin.Close()
	}
}

func projectEvent(event schema.Event, mode projection.Mode) (schema.Event, error) {
	copyEvent := event
	if event.Content == nil {
		return copyEvent, nil
	}
	if event.Content.Mode != schema.ContentFull || len(event.Content.Full) == 0 {
		if mode == projection.Mode(event.Content.Mode) {
			content := *event.Content
			copyEvent.Content = &content
			return copyEvent, nil
		}
		return schema.Event{}, errors.New("pitot: cannot widen an already projected event")
	}
	var raw string
	if err := json.Unmarshal(event.Content.Full, &raw); err != nil {
		return schema.Event{}, fmt.Errorf("pitot: decode full event content: %w", err)
	}
	content, err := projection.Apply(mode, []byte(raw))
	if err != nil {
		return schema.Event{}, err
	}
	copyEvent.Content = &content
	return copyEvent, nil
}

func sortedKinds(values map[string]config.ControllerConfig) []string {
	kinds := make([]string, 0, len(values))
	for kind := range values {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}
