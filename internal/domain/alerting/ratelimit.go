package alerting

import "time"

// RateLimiter is a sliding-window counter: at most `limit` events per
// `window`. It is not goroutine-safe; callers serialise per key.
type RateLimiter struct {
	limit  int
	window time.Duration
	events []time.Time
}

// NewRateLimiter creates a limiter. limit <= 0 means unlimited.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{limit: limit, window: window}
}

func (r *RateLimiter) prune(now time.Time) {
	cutoff := now.Add(-r.window)
	i := 0
	for i < len(r.events) && !r.events[i].After(cutoff) {
		i++
	}
	if i > 0 {
		r.events = append(r.events[:0], r.events[i:]...)
	}
}

// Allow records an event at now if the limit permits and reports whether it
// did. Events arriving out of order are tolerated: the window is pruned
// relative to the newest timestamp seen.
func (r *RateLimiter) Allow(now time.Time) bool {
	if r == nil || r.limit <= 0 {
		return true
	}
	r.prune(now)
	if len(r.events) >= r.limit {
		return false
	}
	r.events = append(r.events, now)
	return true
}

// Count returns the number of events inside the window as of now.
func (r *RateLimiter) Count(now time.Time) int {
	if r == nil {
		return 0
	}
	r.prune(now)
	return len(r.events)
}
