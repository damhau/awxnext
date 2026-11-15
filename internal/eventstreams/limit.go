package eventstreams

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter limits each stream to its events per minute, on this node. A
// minute's events may arrive at once.
type Limiter struct {
	mu      sync.Mutex
	streams map[int64]*streamLimit
}

type streamLimit struct {
	perMinute int
	limiter   *rate.Limiter
}

// NewLimiter returns a Limiter with every stream's budget full.
func NewLimiter() *Limiter { return &Limiter{streams: map[int64]*streamLimit{}} }

// Take takes n events from a stream's budget of perMinute (0: no limit).
// It returns how long to wait when the budget does not have them yet, and
// false when n is more than the stream ever takes at once.
func (l *Limiter) Take(stream int64, perMinute, n int, now time.Time) (time.Duration, bool) {
	if perMinute <= 0 || n == 0 {
		return 0, true
	}
	l.mu.Lock()
	s := l.streams[stream]
	if s == nil || s.perMinute != perMinute {
		s = &streamLimit{perMinute: perMinute, limiter: rate.NewLimiter(rate.Limit(float64(perMinute)/60), perMinute)}
		l.streams[stream] = s
	}
	l.mu.Unlock()
	r := s.limiter.ReserveN(now, n)
	if !r.OK() {
		return 0, false
	}
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return d, true
	}
	return 0, true
}
