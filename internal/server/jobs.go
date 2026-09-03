package server

import (
	"context"
	"sync"
	"time"

	"github.com/rclod/codewalk/internal/agent"
)

// jobManager tracks in-flight walkthrough generations.
//
// Generation takes minutes, so the API is asynchronous from the start: a client
// creates a job, watches its progress, then fetches the finished walkthrough.
// Making that the default shape means adding streaming, queueing or remote
// execution later does not change the API contract.
type jobManager struct {
	mu   sync.Mutex
	jobs map[string]*job
	// retain bounds how long finished jobs stay queryable.
	retain time.Duration
}

// job is one generation in progress or recently finished.
type job struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"` // running | complete | failed | cancelled
	RunID     string     `json:"run_id,omitempty"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`

	mu          sync.Mutex
	events      []agent.Event
	subscribers map[chan agent.Event]struct{}
	done        chan struct{}
	cancel      context.CancelFunc
}

func newJobManager() *jobManager {
	return &jobManager{jobs: map[string]*job{}, retain: time.Hour}
}

func (m *jobManager) create(id string, cancel context.CancelFunc) *job {
	j := &job{
		ID:          id,
		Status:      "running",
		CreatedAt:   time.Now().UTC(),
		subscribers: map[chan agent.Event]struct{}{},
		done:        make(chan struct{}),
		cancel:      cancel,
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	m.sweep()
	return j
}

func (m *jobManager) get(id string) (*job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

func (m *jobManager) list() []*job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	return out
}

// sweep drops finished jobs that are older than the retention window. Completed
// walkthroughs live in the run store, so nothing is lost.
func (m *jobManager) sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-m.retain)
	for id, j := range m.jobs {
		if j.EndedAt != nil && j.EndedAt.Before(cutoff) {
			delete(m.jobs, id)
		}
	}
}

// OnEvent implements agent.Observer, fanning progress out to subscribers and
// buffering it so a client that connects late still sees what happened.
func (j *job) OnEvent(e agent.Event) {
	j.mu.Lock()
	if len(j.events) < 2000 {
		j.events = append(j.events, e)
	}
	subs := make([]chan agent.Event, 0, len(j.subscribers))
	for ch := range j.subscribers {
		subs = append(subs, ch)
	}
	j.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// A slow client must not stall generation; it will still receive
			// the final status.
		}
	}
}

// subscribe returns buffered events and a channel of subsequent ones.
func (j *job) subscribe() ([]agent.Event, chan agent.Event) {
	ch := make(chan agent.Event, 64)
	j.mu.Lock()
	defer j.mu.Unlock()
	history := make([]agent.Event, len(j.events))
	copy(history, j.events)
	j.subscribers[ch] = struct{}{}
	return history, ch
}

func (j *job) unsubscribe(ch chan agent.Event) {
	j.mu.Lock()
	delete(j.subscribers, ch)
	j.mu.Unlock()
}

func (j *job) finish(status, runID, errMsg string) {
	j.mu.Lock()
	now := time.Now().UTC()
	j.Status = status
	j.RunID = runID
	j.Error = errMsg
	j.EndedAt = &now
	subs := make([]chan agent.Event, 0, len(j.subscribers))
	for ch := range j.subscribers {
		subs = append(subs, ch)
	}
	j.subscribers = map[chan agent.Event]struct{}{}
	j.mu.Unlock()

	close(j.done)
	for _, ch := range subs {
		close(ch)
	}
}

// jobStatus is the serialisable view of a job, without its synchronisation
// state.
type jobStatus struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	RunID     string     `json:"run_id,omitempty"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

func (j *job) snapshot() jobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return jobStatus{
		ID: j.ID, Status: j.Status, RunID: j.RunID, Error: j.Error,
		CreatedAt: j.CreatedAt, EndedAt: j.EndedAt,
	}
}
