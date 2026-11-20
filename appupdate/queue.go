package appupdate

import "sync"

// queue hands each state to the listeners in order on one goroutine, so a
// listener may call the updater back without waiting on it.
type queue struct {
	mu        sync.Mutex
	pending   []State
	listeners []func(State)
	running   bool
}

func newQueue() *queue { return &queue{} }

func (q *queue) subscribe(f func(State)) {
	q.mu.Lock()
	q.listeners = append(q.listeners, f)
	q.mu.Unlock()
}

func (q *queue) push(s State) {
	q.mu.Lock()
	q.pending = append(q.pending, s)
	if q.running {
		q.mu.Unlock()
		return
	}
	q.running = true
	q.mu.Unlock()
	go q.drain()
}

func (q *queue) drain() {
	for {
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		s := q.pending[0]
		q.pending = q.pending[1:]
		ls := q.listeners
		q.mu.Unlock()
		for _, f := range ls {
			f(s)
		}
	}
}
