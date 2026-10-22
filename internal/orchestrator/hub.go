package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
)

// errNoLease is the refusal of every worker request about a job the worker does not hold.
func errNoLease(jobID string) error {
	return fmt.Errorf("permission_denied: worker holds no lease on job %s", jobID)
}

// ErrNotAttached is returned when the worker that holds a job has no Attach stream here.
var ErrNotAttached = errors.New("worker not attached to this orchestrator")

// connBuffer bounds the messages queued for one worker; a full stream drops hints (wake-ups,
// subscriptions), which the protocol tolerates.
const connBuffer = 256

// Hub is the Attach side of an orchestrator: it routes wake-ups, live subscriptions and
// cancel requests only to the worker that owns the job, and accepts job events and live
// chunks only from it (R-FLEET-002).
type Hub struct {
	q     *Queue
	mu    sync.Mutex
	conns map[string]*Conn
}

// NewHub performs no I/O.
func NewHub(q *Queue) *Hub { return &Hub{q: q, conns: map[string]*Conn{}} }

// Conn is one authenticated Attach stream of worker wid.
type Conn struct {
	hub  *Hub
	wid  string
	ch   chan string
	free atomic.Int64
}

// Attach registers the stream of an already authenticated worker. A second Attach of the
// same worker replaces the first.
func (h *Hub) Attach(wid string) *Conn {
	c := &Conn{hub: h, wid: wid, ch: make(chan string, connBuffer)}
	h.mu.Lock()
	h.conns[wid] = c
	h.mu.Unlock()
	return c
}

// Detach removes the stream (it ended).
func (c *Conn) Detach() {
	c.hub.mu.Lock()
	if c.hub.conns[c.wid] == c {
		delete(c.hub.conns, c.wid)
	}
	c.hub.mu.Unlock()
}

// Workers returns the attached workers as wake-up targets for a Dispatcher.
func (h *Hub) Workers() []Wake {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Wake, 0, len(h.conns))
	for _, c := range h.conns {
		out = append(out, c)
	}
	return out
}

func (c *Conn) send(m string) {
	select {
	case c.ch <- m:
	default:
	}
}

// Recv delivers what the orchestrator sends to this worker as text lines:
// "wake <class> <slot> <job>", "subscribe <job>", "cancel <job>", "drain".
func (c *Conn) Recv() <-chan string { return c.ch }

// ID is the worker id of the stream.
func (c *Conn) ID() string { return c.wid }

// SetSlots records the worker's last SlotState.
func (c *Conn) SetSlots(free int) { c.free.Store(int64(free)) }

// FreeSlots is the free slot count of the worker's last SlotState.
func (c *Conn) FreeSlots() int { return int(c.free.Load()) }

// Wake sends a wake-up to this worker.
func (c *Conn) Wake(class string, slot uint64, job string) {
	c.send("wake " + class + " " + strconv.FormatUint(slot, 10) + " " + job)
}

// holds checks that this worker owns jobID at epoch (owner + epoch on job/<id>).
func (c *Conn) holds(ctx context.Context, jobID string, epoch int64) error {
	v, err := c.hub.q.Job(ctx, jobID)
	if err != nil || v.Owner != c.wid || v.Epoch != epoch {
		return errNoLease(jobID)
	}
	return nil
}

// JobEvent is a hint from the worker. For a job the worker does not hold (owner + epoch) it
// fails with "permission_denied: worker holds no lease on job <id>".
func (c *Conn) JobEvent(ctx context.Context, jobID string, epoch int64, kind string) error {
	return c.holds(ctx, jobID, epoch)
}

// LiveChunk carries log lines of a job; the same ownership rule as JobEvent.
func (c *Conn) LiveChunk(ctx context.Context, jobID string, epoch int64) error {
	return c.holds(ctx, jobID, epoch)
}

// owner returns the stream of the worker that holds jobID.
func (h *Hub) owner(ctx context.Context, jobID string) (*Conn, error) {
	v, err := h.q.Job(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if v.Owner == "" {
		return nil, fmt.Errorf("job %s has no owner (state %s)", jobID, v.State)
	}
	h.mu.Lock()
	c := h.conns[v.Owner]
	h.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("job %s: %w", jobID, ErrNotAttached)
	}
	return c, nil
}

// Subscribe asks the owner of jobID for its live log.
func (h *Hub) Subscribe(ctx context.Context, jobID string) error {
	c, err := h.owner(ctx, jobID)
	if err != nil {
		return err
	}
	c.send("subscribe " + jobID)
	return nil
}

// CancelJob sends a cancel request to the owner of jobID.
func (h *Hub) CancelJob(ctx context.Context, jobID string) error {
	c, err := h.owner(ctx, jobID)
	if err != nil {
		return err
	}
	c.send("cancel " + jobID)
	return nil
}

// Drain asks worker wid to finish its jobs and claim nothing new.
func (h *Hub) Drain(wid string) error {
	h.mu.Lock()
	c := h.conns[wid]
	h.mu.Unlock()
	if c == nil {
		return fmt.Errorf("worker %s: %w", wid, ErrNotAttached)
	}
	c.send("drain")
	return nil
}
