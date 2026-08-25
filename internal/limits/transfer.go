// Package limits provides dependency-free admission, quota, and rate controls
// for the netspeedd public service surface.
package limits

import "sync"

// TransferRejection identifies which transfer concurrency ceiling rejected a
// request. The zero value means the request was admitted.
type TransferRejection uint8

const (
	TransferAdmitted TransferRejection = iota
	TransferRejectedGlobal
	TransferRejectedClient
)

// TransferLimiter enforces global and per-client active request ceilings.
// Admission is immediate: callers should return an overload response rather
// than queueing measurement requests and corrupting their timing.
type TransferLimiter struct {
	mu           sync.Mutex
	globalMax    int
	perClientMax int
	active       int
	clients      map[string]int
}

// NewTransferLimiter constructs a limiter. Both ceilings must be positive.
func NewTransferLimiter(globalMax, perClientMax int) *TransferLimiter {
	return &TransferLimiter{
		globalMax:    globalMax,
		perClientMax: perClientMax,
		clients:      make(map[string]int),
	}
}

// Acquire reserves one active transfer slot for clientKey.
func (limiter *TransferLimiter) Acquire(clientKey string) (release func(), rejection TransferRejection) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	if limiter.active >= limiter.globalMax {
		return nil, TransferRejectedGlobal
	}
	if limiter.clients[clientKey] >= limiter.perClientMax {
		return nil, TransferRejectedClient
	}

	limiter.active++
	limiter.clients[clientKey]++

	var once sync.Once
	return func() {
		once.Do(func() {
			limiter.mu.Lock()
			defer limiter.mu.Unlock()
			limiter.active--
			remaining := limiter.clients[clientKey] - 1
			if remaining <= 0 {
				delete(limiter.clients, clientKey)
			} else {
				limiter.clients[clientKey] = remaining
			}
		})
	}, TransferAdmitted
}

// Active returns the current global active-transfer count.
func (limiter *TransferLimiter) Active() int {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.active
}

// ActiveFor returns the current active-transfer count for clientKey.
func (limiter *TransferLimiter) ActiveFor(clientKey string) int {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.clients[clientKey]
}
