package computeruse

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type delivery struct {
	outcome Outcome
	err     error
}
type pending struct {
	request  Request
	ctx      context.Context
	deadline time.Time
	claimed  bool
	result   chan delivery
}

type Broker struct {
	mode                           string
	targetRevision                 uint64
	target                         *WindowTarget
	windows                        map[string]WindowTarget
	frameID                        string
	mu                             sync.Mutex
	namespace, run, owner, session string
	leaseUntil                     time.Time
	sessionContext                 context.Context
	sessionCancel                  context.CancelFunc
	pending                        *pending
	closed                         bool
	done                           chan struct{}
	vision                         func() bool
}

func New(namespace, run string) *Broker {
	b := &Broker{namespace: namespace, run: run, done: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-b.done:
				return
			case <-ticker.C:
				b.mu.Lock()
				b.expire()
				b.mu.Unlock()
			}
		}
	}()
	return b
}

func (b *Broker) SetVisionAvailable(fn func() bool) { b.mu.Lock(); defer b.mu.Unlock(); b.vision = fn }

func (b *Broker) cancel() {
	if b.pending != nil {
		b.pending.result <- delivery{err: ErrRejected}
		b.pending = nil
	}
}

func (b *Broker) drop() {
	b.cancel()
	if b.sessionCancel != nil {
		b.sessionCancel()
	}
	b.sessionContext = nil
	b.sessionCancel = nil
	b.mode = ""
	b.targetRevision = 0
	b.target = nil
	b.windows = nil
	b.frameID = ""
	b.owner = ""
	b.session = ""
}

func (b *Broker) rejectPending() {
	if b.pending != nil && b.pending.claimed {
		b.drop()
	} else {
		b.cancel()
	}
}

type sessionContextKey struct{}

// SessionContext pins work to this attachment, even after its capture request resolves.
func (b *Broker) SessionContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire()
	if b.closed || b.session == "" || ctx.Err() != nil {
		return nil, nil, ErrRejected
	}
	ctx, cancel := context.WithCancel(context.WithValue(ctx, sessionContextKey{}, b.sessionContext))
	stop := context.AfterFunc(b.sessionContext, cancel)
	return ctx, func() { stop(); cancel() }, nil
}

func (b *Broker) sessionValid(ctx context.Context) bool {
	return !b.closed && b.session != "" && ctx.Err() == nil && ctx.Value(sessionContextKey{}) == b.sessionContext
}

func (b *Broker) SessionValid(ctx context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire()
	return b.sessionValid(ctx)
}

func (b *Broker) expire() {
	now := time.Now()
	if b.session != "" && !now.Before(b.leaseUntil) {
		b.drop()
	}
	if b.pending != nil && (b.pending.ctx.Err() != nil || !now.Before(b.pending.deadline)) {
		b.rejectPending()
	}
}

func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.drop()
		close(b.done)
	}
	return nil
}

func (b *Broker) Active() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire()
	return !b.closed && b.session != ""
}

func (b *Broker) Exchange(e Exchange) (Response, error) {
	if err := e.Validate(); err != nil {
		return Response{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire()
	if b.closed || e.Namespace != b.namespace || e.Run != b.run {
		return Response{}, ErrRejected
	}
	if b.session != "" && (b.owner != e.Owner || b.session != e.SessionID) {
		return Response{}, ErrRejected
	}
	mode := "selected_window"
	if e.Operation == "attach_agent" {
		mode = "agent_choice"
	}
	if b.session != "" && (e.Operation == "attach" || e.Operation == "attach_agent") && b.mode != mode {
		return Response{}, ErrRejected
	}
	if (e.Operation == "attach" || e.Operation == "attach_agent") && b.session == "" {
		b.mode = mode
		b.owner = e.Owner
		b.session = e.SessionID
		b.sessionContext, b.sessionCancel = context.WithCancel(context.Background())
	}
	available := b.vision != nil && b.vision()
	if b.session == "" {
		if e.Operation == "claim" || e.Operation == "resolve" {
			return Response{}, ErrRejected
		}
		return Response{Reason: "session inactive", VisionAvailable: available}, nil
	}
	switch e.Operation {
	case "attach", "attach_agent", "poll":
		b.leaseUntil = time.Now().Add(Lease)
	case "stop":
		b.drop()
		return Response{Reason: "session stopped", VisionAvailable: available}, nil
	case "claim":
		p := b.pending
		if p == nil || p.request.RequestID != e.RequestID || p.claimed {
			return Response{}, ErrRejected
		}
		p.claimed = true
		p.deadline = minTime(p.deadline, time.Now().Add(ClaimTimeoutFor(p.request.Action)))
	case "resolve":
		p := b.pending
		if p == nil || p.request.RequestID != e.RequestID || !p.claimed {
			return Response{}, ErrRejected
		}
		if e.Outcome.Capture != nil && p.request.Action.Kind != "observe" {
			return Response{}, ErrRejected
		}
		if p.request.Action.Kind == "observe" && e.Outcome.Status == "completed" && e.Outcome.Capture == nil {
			return Response{}, ErrRejected
		}
		o := e.Outcome
		kind := p.request.Action.Kind
		if (kind != "list_windows" && len(o.Windows) != 0) || (kind != "select_window" && o.Target != nil) {
			return Response{}, ErrRejected
		}
		if b.mode == "agent_choice" {
			expected := b.targetRevision
			if kind == "select_window" && o.Status == "completed" {
				expected++
			}
			if o.TargetRevision != expected {
				return Response{}, ErrRejected
			}
			if o.Status == "completed" {
				switch kind {
				case "list_windows":
					b.windows = map[string]WindowTarget{}
					for _, w := range o.Windows {
						b.windows[w.Ref] = w
					}
				case "select_window":
					w, ok := b.windows[p.request.Action.TargetRef]
					if !ok || o.Target == nil || *o.Target != w {
						return Response{}, ErrRejected
					}
					b.target = o.Target
					b.targetRevision = expected
					b.frameID = ""
					b.windows = nil
				case "observe":
					b.frameID = o.Capture.FrameID
				default:
					b.frameID = ""
				}
			}
		} else if o.TargetRevision != 0 || o.Target != nil || len(o.Windows) != 0 {
			return Response{}, ErrRejected
		}
		p.result <- delivery{outcome: *e.Outcome}
		b.pending = nil
	}
	r := Response{Active: true, VisionAvailable: available}
	if b.mode == "agent_choice" {
		r.Mode = b.mode
	}
	if b.pending != nil && !b.pending.claimed {
		copy := b.pending.request
		r.Pending = &copy
	}
	return r, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (b *Broker) Request(ctx context.Context, action Action, frameID string) (Outcome, error) {
	// wait is an agent-side pause, never a desktop request.
	if action.Kind == "wait" || action.Validate() != nil || (frameID != "" && !identifier.MatchString(frameID)) {
		return Outcome{}, ErrRejected
	}
	if ctx.Value(sessionContextKey{}) == nil {
		var cancel context.CancelFunc
		var err error
		ctx, cancel, err = b.SessionContext(ctx)
		if err != nil {
			return Outcome{}, err
		}
		defer cancel()
	}
	b.mu.Lock()
	b.expire()
	if !b.sessionValid(ctx) || b.pending != nil {
		b.mu.Unlock()
		return Outcome{}, ErrRejected
	}
	discovery := action.Kind == "list_windows" || action.Kind == "select_window"
	if discovery && (b.mode != "agent_choice" || frameID != "") {
		b.mu.Unlock()
		return Outcome{}, ErrRejected
	}
	if b.mode == "agent_choice" {
		if action.Kind == "select_window" {
			if _, ok := b.windows[action.TargetRef]; !ok {
				b.mu.Unlock()
				return Outcome{}, ErrRejected
			}
		}
		if !discovery && b.target == nil {
			b.mu.Unlock()
			return Outcome{}, ErrRejected
		}
		if action.IsInput() && (frameID == "" || frameID != b.frameID) {
			b.mu.Unlock()
			return Outcome{}, ErrRejected
		}
		if action.Kind == "open_url" {
			b.mu.Unlock()
			return Outcome{}, ErrRejected
		}
	}
	p := &pending{request: Request{TargetRevision: b.targetRevision, RequestID: uuid.NewString(), FrameID: frameID, Action: action}, ctx: ctx, deadline: time.Now().Add(RequestTimeout), result: make(chan delivery, 1)}
	b.pending = p
	b.mu.Unlock()
	select {
	case d := <-p.result:
		if !b.SessionValid(ctx) {
			return Outcome{}, ErrRejected
		}
		return d.outcome, d.err
	case <-ctx.Done():
		b.mu.Lock()
		if b.pending == p {
			b.rejectPending()
		}
		b.mu.Unlock()
		return Outcome{}, ErrRejected
	}
}

type contextKey struct{}

func WithBroker(ctx context.Context, b *Broker) context.Context {
	return context.WithValue(ctx, contextKey{}, b)
}
func FromContext(ctx context.Context) *Broker { b, _ := ctx.Value(contextKey{}).(*Broker); return b }
