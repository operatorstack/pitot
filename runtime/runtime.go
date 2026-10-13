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

// Decision is a resolved controller outcome surfaced to an optional observer.
// It carries no policy meaning; it is a transport receipt for tooling such as
// `pitot dev` that renders a decision timeline.
type Decision struct {
	Kind     string
	ActionID string
	Outcome  string
	Message  string
}

// Manager starts configured role processes and exposes their shared delivery path.
type Manager struct {
	ctx               context.Context
	cancel            context.CancelFunc
	stderr            io.Writer
	controllers       map[string]*controllerWorker
	consumers         []*consumerWorker
	observer          func(Decision)
	requireController bool
	observedKindsMu   sync.Mutex
	observedKinds     map[string]struct{}
}

// ErrControllerRequired reports a kind delivered without a registered
// Controller while require_controller (or --strict) is active.
var ErrControllerRequired = errors.New("pitot: no controller registered for this action kind and require_controller is active")

// SetDecisionObserver registers a callback invoked for every resolved controller
// decision. It is optional; a nil observer disables receipts. Not safe to change
// concurrently with active delivery.
func (m *Manager) SetDecisionObserver(observer func(Decision)) {
	m.observer = observer
}

func (m *Manager) reportDecision(kind string, response *schema.ControlResponse) {
	if m.observer == nil || response == nil {
		return
	}
	m.observer(Decision{
		Kind:     kind,
		ActionID: response.ActionID,
		Outcome:  response.Outcome,
		Message:  response.Message,
	})
}

// Start creates the complete configured process boundary. A child that cannot
// start remains unavailable so the declared default can resolve requests.
func Start(parent context.Context, cfg config.Config, stderr io.Writer) (*Manager, error) {
	ctx, cancel := context.WithCancel(parent)
	manager := &Manager{
		ctx:               ctx,
		cancel:            cancel,
		stderr:            stderr,
		controllers:       map[string]*controllerWorker{},
		requireController: cfg.RequireController,
		observedKinds:     map[string]struct{}{},
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
		worker := newControllerWorker(ctx, router, registration, declared, stderr)
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
	return m.deliverEvent(ctx, event, false)
}

// DeliverControlledEvent requires an actual Controller reply. Configured
// timeout/unavailable defaults and observation-only delivery cannot grant it.
func (m *Manager) DeliverControlledEvent(ctx context.Context, event schema.Event) (*schema.ControlResponse, error) {
	return m.deliverEvent(ctx, event, true)
}

func (m *Manager) deliverEvent(ctx context.Context, event schema.Event, explicit bool) (*schema.ControlResponse, error) {
	if err := schema.ValidateEvent(event); err != nil {
		return nil, err
	}
	if event.Type != schema.TypeActionRequested {
		return nil, errors.New("pitot: unsupported runtime event type")
	}
	m.offerConsumers(event)
	if event.Action == nil {
		return nil, errors.New("pitot: controllable event requires an action")
	}
	worker, exists := m.controllers[event.Action.Kind]
	if !exists {
		if m.requireController || explicit {
			return nil, ErrControllerRequired
		}
		// Observation-only is a feature; silence about it is not. Report the
		// degradation once per kind on the operator's channel.
		m.observedKindsMu.Lock()
		if _, seen := m.observedKinds[event.Action.Kind]; !seen {
			m.observedKinds[event.Action.Kind] = struct{}{}
			fmt.Fprintf(m.stderr, "pitot: action kind has no controller; observation-only (set require_controller: true or run with --strict to fault instead)\n")
		}
		m.observedKindsMu.Unlock()
		return nil, nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("pitot: encode controller event: %w", err)
	}
	response, resolveErr := worker.resolveMode(ctx, schema.ControlRequested{
		PitotVersion: schema.Version,
		Type:         schema.TypeControlRequested,
		Kind:         event.Action.Kind,
		ActionID:     event.Action.ID,
		Data:         data,
	}, explicit)
	if explicit && resolveErr != nil {
		return nil, resolveErr
	}
	m.reportDecision(event.Action.Kind, &response)
	return &response, resolveErr
}

// offerConsumers reuses projection work only among subscribers to this event.
// Queued Content values and full-content bytes remain independently owned.
func (m *Manager) offerConsumers(event schema.Event) {
	if len(m.consumers) < 2 {
		for _, consumer := range m.consumers {
			m.reportConsumerOffer(consumer, consumer.offer(event))
		}
		return
	}
	var counts [3]int
	for _, consumer := range m.consumers {
		if _, subscribed := consumer.events[event.Type]; subscribed {
			if slot := projectionSlot(consumer.projection); slot >= 0 {
				counts[slot]++
			}
		}
	}
	var cached [3]struct {
		ready bool
		event schema.Event
		err   error
	}
	for _, consumer := range m.consumers {
		if _, subscribed := consumer.events[event.Type]; !subscribed {
			continue
		}
		slot := projectionSlot(consumer.projection)
		if slot < 0 || counts[slot] < 2 {
			m.reportConsumerOffer(consumer, consumer.offer(event))
			continue
		}
		result := &cached[slot]
		if !result.ready {
			result.event, result.err = projectEvent(event, consumer.projection)
			result.ready = true
		}
		err := result.err
		if err == nil {
			projected := result.event
			if projected.Content != nil {
				content := *projected.Content
				content.Full = append(json.RawMessage(nil), content.Full...)
				projected.Content = &content
			}
			err = consumer.enqueue(projected)
		}
		m.reportConsumerOffer(consumer, err)
	}
}

func (m *Manager) reportConsumerOffer(consumer *consumerWorker, err error) {
	if err != nil {
		fmt.Fprintf(m.stderr, "pitot: consumer %q delivery fault: %v\n", consumer.id, err)
	}
}

func projectionSlot(mode projection.Mode) int {
	switch mode {
	case projection.Full:
		return 0
	case projection.SHA256:
		return 1
	case projection.Omit:
		return 2
	default:
		return -1
	}
}

// Request routes an explicit request through the same Controller worker used by hooks.
func (m *Manager) Request(ctx context.Context, request schema.ControlRequested) (schema.ControlResponse, error) {
	if err := schema.ValidateRequest(request); err != nil {
		return schema.ControlResponse{}, err
	}
	worker, exists := m.controllers[request.Kind]
	if !exists {
		return schema.ControlResponse{}, bridge.ErrNoController
	}
	response, err := worker.resolve(ctx, request)
	m.reportDecision(request.Kind, &response)
	return response, err
}

type controllerResult struct {
	response schema.ControlResponse
	err      error
}

type controllerWorker struct {
	ctx          context.Context
	cancel       context.CancelFunc
	router       *bridge.Router
	registration bridge.Registration
	stdin        io.WriteCloser
	responses    chan controllerResult
	done         chan error
	startErr     error
	slot         chan struct{}
	pending      sync.Map
	resolved     map[string]struct{}
	resolvedFIFO []string
	closeOnce    sync.Once
}

func newControllerWorker(ctx context.Context, router *bridge.Router, registration bridge.Registration, declared config.ControllerConfig, stderr io.Writer) *controllerWorker {
	ctx, cancel := context.WithCancel(ctx)
	worker := &controllerWorker{
		ctx:          ctx,
		cancel:       cancel,
		router:       router,
		registration: registration,
		responses:    make(chan controllerResult, 16),
		done:         make(chan error, 1),
		resolved:     map[string]struct{}{},
		slot:         make(chan struct{}, 1),
	}
	cmd := exec.CommandContext(ctx, declared.Command[0], declared.Command[1:]...)
	cmd.Dir = declared.Dir
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		worker.startErr = err
		cancel()
		return worker
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		worker.startErr = err
		cancel()
		return worker
	}
	if err := cmd.Start(); err != nil {
		worker.startErr = err
		cancel()
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
		if err := schema.DecodeEnvelope(scanner.Bytes(), &response); err != nil {
			select {
			case w.responses <- controllerResult{err: err}:
			case <-w.ctx.Done():
			}
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
	return w.resolveMode(ctx, request, false)
}

func (w *controllerWorker) resolveMode(ctx context.Context, request schema.ControlRequested, requireReply bool) (response schema.ControlResponse, err error) {
	explicitReply := false
	defer func() {
		if requireReply && !explicitReply {
			err = errors.Join(err, errors.New("pitot: explicit Controller reply required"))
		}
	}()
	// A cancelled duplicate must not resolve the action owned by another caller.
	if _, pending := w.pending.LoadOrStore(request.ActionID, struct{}{}); pending {
		return schema.ControlResponse{}, bridge.ErrDuplicate
	}
	defer w.pending.Delete(request.ActionID)
	// The budget starts at admission, including time spent behind another request.
	expires := time.Now().Add(time.Duration(w.registration.DeadlineMS) * time.Millisecond)
	deadline := time.NewTimer(time.Until(expires))
	defer deadline.Stop()
	timeout := func(cause error) (schema.ControlResponse, error) {
		response, err := w.router.TimeoutResponse(request)
		return response, errors.Join(cause, err)
	}
	if err := ctx.Err(); err != nil {
		return timeout(err)
	}
	select {
	case w.slot <- struct{}{}:
	default:
		select {
		case w.slot <- struct{}{}:
		case <-deadline.C:
			return timeout(nil)
		case <-ctx.Done():
			return timeout(ctx.Err())
		case <-w.ctx.Done():
			response, err := w.router.Resolve(request, nil)
			return response, errors.Join(w.ctx.Err(), err)
		}
	}
	defer func() { <-w.slot }()
	// A ready slot must not let an already expired request reach the child pipe.
	if err := ctx.Err(); err != nil {
		return timeout(err)
	}
	if !time.Now().Before(expires) {
		return timeout(nil)
	}
	if w.startErr != nil || w.stdin == nil {
		response, err := w.router.Resolve(request, nil)
		return response, errors.Join(w.startErr, err)
	}
	if err := w.ctx.Err(); err != nil {
		response, resolveErr := w.router.Resolve(request, nil)
		return response, errors.Join(err, resolveErr)
	}
	// Closing the owned pipe interrupts a stalled write. Join the writer before
	// releasing the slot, and retire this transport because its frame may be partial.
	written := make(chan error, 1)
	go func() { written <- protocol.WriteLine(w.stdin, request) }()
	interruptWrite := func(cause error) {
		w.startErr = errors.Join(errors.New("controller request write interrupted"), cause)
		w.close()
		<-written
		w.remember(request.ActionID)
	}
	select {
	case err := <-written:
		if err != nil {
			w.startErr = err
			w.close()
			response, resolveErr := w.router.Resolve(request, nil)
			w.remember(request.ActionID)
			return response, errors.Join(err, resolveErr)
		}
	case <-deadline.C:
		interruptWrite(context.DeadlineExceeded)
		return timeout(nil)
	case <-ctx.Done():
		interruptWrite(ctx.Err())
		return timeout(ctx.Err())
	case <-w.ctx.Done():
		interruptWrite(w.ctx.Err())
		response, err := w.router.Resolve(request, nil)
		return response, errors.Join(w.ctx.Err(), err)
	}
	for {
		// A stream of stale responses cannot keep an expired request alive.
		if err := ctx.Err(); err != nil {
			w.remember(request.ActionID)
			return timeout(err)
		}
		if !time.Now().Before(expires) {
			w.remember(request.ActionID)
			return timeout(nil)
		}
		select {
		case result := <-w.responses:
			if result.err != nil {
				w.startErr = result.err
				w.close()
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
			explicitReply = err == nil
			return response, err
		case err := <-w.done:
			if err == nil {
				err = errors.New("controller exited")
			}
			w.startErr = err
			w.close()
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
		if w.cancel != nil {
			w.cancel()
		}
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
	stateMu    sync.Mutex
	stderr     io.Writer
}

func newConsumerWorker(ctx context.Context, declared config.ConsumerConfig, stderr io.Writer) *consumerWorker {
	worker := &consumerWorker{
		id:         declared.ID,
		events:     map[string]struct{}{},
		projection: declared.Projection.Content,
		queue:      make(chan schema.Event, consumerQueueSize),
		dead:       make(chan struct{}),
		stderr:     stderr,
	}
	for _, event := range declared.Events {
		worker.events[event] = struct{}{}
	}
	cmd := exec.CommandContext(ctx, declared.Command[0], declared.Command[1:]...)
	cmd.Dir = declared.Dir
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
		if ctx.Err() == nil {
			worker.stop("process-exited")
		} else {
			worker.stop("")
		}
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
	return w.enqueue(projected)
}

func (w *consumerWorker) enqueue(projected schema.Event) error {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	select {
	case <-w.dead:
		return errors.New("consumer process is unavailable")
	default:
	}
	select {
	case w.queue <- projected:
		return nil
	default:
		return errors.New("consumer queue is full")
	}
}

func (w *consumerWorker) deliver(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			w.stop("")
			return
		}
		select {
		case event := <-w.queue:
			if ctx.Err() != nil {
				w.stop("")
				return
			}
			select {
			case <-w.dead:
				return
			default:
			}
			if err := protocol.WriteLine(w.stdin, event); err != nil {
				if ctx.Err() == nil {
					w.stop("write-failed")
				} else {
					w.stop("")
				}
				return
			}
		case <-ctx.Done():
			w.stop("")
			return
		case <-w.dead:
			return
		}
	}
}

// stop publishes terminal state before reporting it. Neither pipe writes nor
// diagnostics hold stateMu, so admission cannot wait for a blocked child/sink.
func (w *consumerWorker) stop(reason string) {
	w.stateMu.Lock()
	select {
	case <-w.dead:
		w.stateMu.Unlock()
		return
	default:
		close(w.dead)
	}
	w.stateMu.Unlock()
	if reason != "" && w.stderr != nil {
		fmt.Fprintf(w.stderr, "pitot: consumer %q delivery fault: %s; observation delivery may be incomplete\n", w.id, reason)
	}
}

func (w *consumerWorker) close() {
	w.stop("")
	if w.stdin != nil {
		_ = w.stdin.Close()
	}
}

func projectEvent(event schema.Event, mode projection.Mode) (schema.Event, error) {
	if !mode.Valid() {
		return schema.Event{}, errors.New("pitot: invalid content projection")
	}
	if event.Content != nil {
		if err := schema.ValidateContent(*event.Content); err != nil {
			return schema.Event{}, err
		}
	}
	copyEvent := event
	if event.Content == nil {
		return copyEvent, nil
	}
	if event.Content.Mode != schema.ContentFull || len(event.Content.Full) == 0 {
		if mode == projection.Omit {
			copyEvent.Content = &schema.Content{Mode: schema.ContentOmit}
			return copyEvent, nil
		}
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
