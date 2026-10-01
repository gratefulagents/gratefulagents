package computeruse

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Timeouts are variables so tests can shorten them.
var (
	LeaseDuration    = 30 * time.Second
	PollWait         = 20 * time.Second
	PickupTimeout    = 30 * time.Second
	DeliveredTimeout = 300 * time.Second
	expiryTick       = 50 * time.Millisecond
)

type outcome struct {
	result Result
	err    error
}

type pending struct {
	request   Request
	delivered bool
	deadline  time.Time
	done      chan outcome
}

// Broker relays computer_use actions from the agent to one desktop session.
type Broker struct {
	mu             sync.Mutex
	namespace, run string
	owner, session string
	leaseUntil     time.Time
	pending        *pending
	// screenW/screenH are the latest full screenshot of this session.
	screenW, screenH int
	// polling counts waiting long-polls; an open poll keeps the lease alive.
	polling   int
	wake      chan struct{}
	closed    bool
	done      chan struct{}
	available func() bool
}

func New(namespace, run string) *Broker {
	b := &Broker{namespace: namespace, run: run, wake: make(chan struct{}), done: make(chan struct{})}
	ticker := time.NewTicker(expiryTick)
	go func() {
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

// SetAvailable installs the predicate reported as Response.Available.
func (b *Broker) SetAvailable(fn func() bool) { b.mu.Lock(); defer b.mu.Unlock(); b.available = fn }

func (b *Broker) notify() {
	close(b.wake)
	b.wake = make(chan struct{})
}

func (b *Broker) fail(err error) {
	if b.pending != nil {
		b.pending.done <- outcome{err: err}
		b.pending = nil
	}
}

func (b *Broker) endSession(err error) {
	b.fail(err)
	b.owner, b.session = "", ""
	b.screenW, b.screenH = 0, 0
	b.notify()
}

func (b *Broker) expire() {
	now := time.Now()
	if p := b.pending; p != nil && !now.Before(p.deadline) {
		if p.delivered {
			b.fail(fmt.Errorf("%w: desktop did not finish the action within %s", ErrTimeout, DeliveredTimeout))
		} else {
			b.fail(fmt.Errorf("%w: desktop did not pick up the request", ErrTimeout))
		}
		b.notify()
	}
	if b.session != "" && b.polling == 0 && !now.Before(b.leaseUntil) && (b.pending == nil || !b.pending.delivered) {
		b.endSession(fmt.Errorf("%w: desktop connection lost", ErrDisconnected))
	}
}

func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.endSession(ErrDisconnected)
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

func (b *Broker) response(active bool, reason string) Response {
	return Response{Protocol: Protocol, Active: active, Available: b.available != nil && b.available(), Reason: reason}
}

func (b *Broker) owns(e Exchange) bool {
	return b.session != "" && b.owner == e.Owner && b.session == e.SessionID
}

// Exchange handles one desktop operation. Refusals are reported in the
// Response; an error means the exchange itself was malformed.
func (b *Broker) Exchange(ctx context.Context, e Exchange) (Response, error) {
	if err := e.Validate(); err != nil {
		return Response{Protocol: Protocol, Reason: ReasonInvalid}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire()
	if e.Namespace != b.namespace || e.Run != b.run {
		return Response{Protocol: Protocol, Reason: ReasonInvalid}, rejected("wrong run")
	}
	if b.closed {
		return b.response(false, ReasonClosed), nil
	}
	switch e.Operation {
	case "connect":
		if b.session != "" && b.owner != e.Owner {
			return b.response(false, ReasonForeignOwner), nil
		}
		if b.session != e.SessionID {
			if b.session != "" {
				b.endSession(fmt.Errorf("%w: desktop reconnected", ErrDisconnected))
			}
			b.owner, b.session = e.Owner, e.SessionID
			b.notify()
		}
		b.leaseUntil = time.Now().Add(LeaseDuration)
		return b.response(true, ""), nil
	case "disconnect":
		if !b.owns(e) {
			return b.response(false, ReasonNoSession), nil
		}
		b.endSession(ErrDisconnected)
		return b.response(false, ReasonDisconnected), nil
	case "result":
		if !b.owns(e) {
			return b.response(false, ReasonNoSession), nil
		}
		b.leaseUntil = time.Now().Add(LeaseDuration)
		p := b.pending
		if p == nil || !p.delivered || p.request.ID != e.RequestID {
			return b.response(true, ReasonStaleResult), nil
		}
		if s := e.Result.Screenshot; s != nil && p.request.Action.Action != "zoom" {
			b.screenW, b.screenH = s.Width, s.Height
		}
		p.done <- outcome{result: *e.Result}
		b.pending = nil
		b.notify()
		return b.response(true, ""), nil
	}
	return b.next(ctx, e)
}

// next long-polls for an undelivered request. Called with b.mu held.
func (b *Broker) next(ctx context.Context, e Exchange) (Response, error) {
	if !b.owns(e) {
		return b.response(false, ReasonNoSession), nil
	}
	timer := time.NewTimer(PollWait)
	defer timer.Stop()
	for {
		b.leaseUntil = time.Now().Add(LeaseDuration)
		// The desktop only polls while idle, so a delivered but unresolved
		// request means the earlier response was lost in transit (exec or
		// network failure): deliver it again rather than strand the agent.
		if p := b.pending; p != nil {
			if !p.delivered {
				p.delivered = true
				p.deadline = time.Now().Add(DeliveredTimeout)
			}
			r := b.response(true, "")
			request := p.request
			r.Request = &request
			return r, nil
		}
		wake := b.wake
		b.polling++
		b.mu.Unlock()
		var stop bool
		select {
		case <-wake:
		case <-timer.C:
			stop = true
		case <-ctx.Done():
			stop = true
		}
		b.mu.Lock()
		b.polling--
		b.expire()
		if b.closed {
			return b.response(false, ReasonClosed), nil
		}
		if !b.owns(e) {
			return b.response(false, ReasonEnded), nil
		}
		if stop {
			b.leaseUntil = time.Now().Add(LeaseDuration)
			return b.response(true, ""), nil
		}
	}
}

// Request sends one action to the desktop and waits for its result. A wait
// action is handled by the caller and rejected here.
func (b *Broker) Request(ctx context.Context, action Action) (Result, error) {
	if action.Action == "wait" {
		return Result{}, rejected("wait is not a desktop action")
	}
	if err := action.Validate(); err != nil {
		return Result{}, err
	}
	b.mu.Lock()
	b.expire()
	if b.closed || b.session == "" {
		b.mu.Unlock()
		return Result{}, ErrNoDesktop
	}
	if b.pending != nil {
		b.mu.Unlock()
		return Result{}, ErrBusy
	}
	if b.screenW > 0 {
		if err := action.CheckBounds(b.screenW, b.screenH); err != nil {
			b.mu.Unlock()
			return Result{}, err
		}
	}
	p := &pending{request: Request{ID: uuid.NewString(), Action: action}, deadline: time.Now().Add(PickupTimeout), done: make(chan outcome, 1)}
	b.pending = p
	b.notify()
	b.mu.Unlock()
	select {
	case o := <-p.done:
		return o.result, o.err
	case <-ctx.Done():
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.pending == p {
			b.pending = nil
			b.notify()
		}
		return Result{}, ctx.Err()
	}
}

type contextKey struct{}

func WithBroker(ctx context.Context, b *Broker) context.Context {
	return context.WithValue(ctx, contextKey{}, b)
}

func FromContext(ctx context.Context) *Broker { b, _ := ctx.Value(contextKey{}).(*Broker); return b }
