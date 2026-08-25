package limits

import (
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// KeyedRateLimiter is a per-key token bucket. A non-positive rate disables it.
type KeyedRateLimiter struct {
	mu         sync.Mutex
	rate       float64
	burst      float64
	now        func() time.Time
	states     map[string]bucket
	operations uint64
	maxEntries int
}

// NewKeyedRateLimiter constructs a limiter with eventsPerSecond refill rate.
func NewKeyedRateLimiter(eventsPerSecond float64, burst int) *KeyedRateLimiter {
	return newKeyedRateLimiter(eventsPerSecond, burst, time.Now)
}

func newKeyedRateLimiter(eventsPerSecond float64, burst int, now func() time.Time) *KeyedRateLimiter {
	return &KeyedRateLimiter{
		rate:       eventsPerSecond,
		burst:      float64(burst),
		now:        now,
		states:     make(map[string]bucket),
		maxEntries: 65_536,
	}
}

// Allow consumes one token. retryAfter is meaningful when allowed is false.
func (limiter *KeyedRateLimiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	if limiter.rate <= 0 || limiter.burst <= 0 {
		return true, 0
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	now := limiter.now()
	limiter.maintainLocked(now, key)
	state, exists := limiter.states[key]
	if !exists {
		state = bucket{tokens: limiter.burst, last: now}
	} else {
		elapsed := now.Sub(state.last).Seconds()
		if elapsed > 0 {
			state.tokens += elapsed * limiter.rate
			if state.tokens > limiter.burst {
				state.tokens = limiter.burst
			}
			state.last = now
		}
	}

	if state.tokens >= 1 {
		state.tokens--
		limiter.states[key] = state
		return true, 0
	}

	limiter.states[key] = state
	missing := 1 - state.tokens
	return false, time.Duration(missing/limiter.rate*float64(time.Second)) + time.Millisecond
}

func (limiter *KeyedRateLimiter) maintainLocked(now time.Time, incomingKey string) {
	limiter.operations++
	fillDuration := time.Duration(0)
	if limiter.rate > 0 {
		fillDuration = time.Duration(limiter.burst / limiter.rate * float64(time.Second))
	}
	staleAfter := 10 * fillDuration
	if staleAfter < 10*time.Minute {
		staleAfter = 10 * time.Minute
	}
	if staleAfter > 24*time.Hour {
		staleAfter = 24 * time.Hour
	}
	if limiter.operations%1024 == 0 || len(limiter.states) >= limiter.maxEntries {
		for key, state := range limiter.states {
			if now.Sub(state.last) >= staleAfter {
				delete(limiter.states, key)
			}
		}
	}
	if limiter.maxEntries <= 0 || len(limiter.states) < limiter.maxEntries {
		return
	}
	if _, exists := limiter.states[incomingKey]; exists {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, state := range limiter.states {
		if oldestKey == "" || state.last.Before(oldest) {
			oldestKey = key
			oldest = state.last
		}
	}
	if oldestKey != "" {
		delete(limiter.states, oldestKey)
	}
}
