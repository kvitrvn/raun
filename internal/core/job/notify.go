package job

import "sync"

// Notifier wakes the claims that wait for a job: a store tells it, after it
// committed, that a job may have become claimable, and every claim waiting
// at that moment tries again. It holds no job: a wake-up that finds nothing
// costs one claim, a missed one at most the rest of a wait. Its zero value
// is ready.
type Notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

// Wait returns a channel closed at the next Notify. A claim takes it before
// it tries, so a job queued between its try and its wait still wakes it.
func (n *Notifier) Wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch == nil {
		n.ch = make(chan struct{})
	}
	return n.ch
}

// Notify wakes every claim waiting now.
func (n *Notifier) Notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch != nil {
		close(n.ch)
		n.ch = nil
	}
}
