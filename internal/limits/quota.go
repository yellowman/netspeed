package limits

import (
	"sync"
	"time"
)

type quotaEntry struct {
	windowStart time.Time
	used        int64
}

// ByteQuota is a fixed-window per-key byte quota. It intentionally charges
// bytes when they are reserved, even if a client disconnects later: an aborted
// transfer still consumed server and network resources.
type ByteQuota struct {
	mu         sync.Mutex
	maxBytes   int64
	window     time.Duration
	now        func() time.Time
	entries    map[string]quotaEntry
	operations uint64
	maxEntries int
}

// QuotaResult describes one reservation attempt.
type QuotaResult struct {
	Allowed    bool
	Remaining  int64
	RetryAfter time.Duration
}

// NewByteQuota constructs a quota. maxBytes <= 0 disables enforcement.
func NewByteQuota(maxBytes int64, window time.Duration) *ByteQuota {
	return newByteQuota(maxBytes, window, time.Now)
}

func newByteQuota(maxBytes int64, window time.Duration, now func() time.Time) *ByteQuota {
	return &ByteQuota{
		maxBytes:   maxBytes,
		window:     window,
		now:        now,
		entries:    make(map[string]quotaEntry),
		maxEntries: 65_536,
	}
}

// Reserve charges n bytes to key when the complete reservation fits.
func (quota *ByteQuota) Reserve(key string, n int64) QuotaResult {
	if n < 0 {
		return QuotaResult{Allowed: false}
	}
	if quota.maxBytes <= 0 || n == 0 {
		return QuotaResult{Allowed: true, Remaining: quota.maxBytes}
	}

	quota.mu.Lock()
	defer quota.mu.Unlock()

	now := quota.now()
	quota.maintainLocked(now, key)
	entry := quota.entries[key]
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= quota.window {
		entry = quotaEntry{windowStart: now}
	}

	if n > quota.maxBytes-entry.used {
		retry := quota.window - now.Sub(entry.windowStart)
		if retry < 0 {
			retry = 0
		}
		quota.entries[key] = entry
		return QuotaResult{
			Allowed:    false,
			Remaining:  quota.maxBytes - entry.used,
			RetryAfter: retry,
		}
	}

	entry.used += n
	quota.entries[key] = entry
	return QuotaResult{
		Allowed:    true,
		Remaining:  quota.maxBytes - entry.used,
		RetryAfter: quota.window - now.Sub(entry.windowStart),
	}
}

// Charge accounts actual bytes already consumed from a stream. When n crosses
// the remaining allowance, the current window is exhausted and Allowed is
// false. This differs from Reserve, which is all-or-nothing for a known future
// transfer.
func (quota *ByteQuota) Charge(key string, n int64) QuotaResult {
	if n < 0 {
		return QuotaResult{Allowed: false}
	}
	if quota.maxBytes <= 0 || n == 0 {
		return QuotaResult{Allowed: true, Remaining: quota.maxBytes}
	}

	quota.mu.Lock()
	defer quota.mu.Unlock()

	now := quota.now()
	quota.maintainLocked(now, key)
	entry := quota.entries[key]
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= quota.window {
		entry = quotaEntry{windowStart: now}
	}
	remaining := quota.maxBytes - entry.used
	allowed := n <= remaining
	if n >= remaining {
		entry.used = quota.maxBytes
	} else {
		entry.used += n
	}
	quota.entries[key] = entry
	retry := quota.window - now.Sub(entry.windowStart)
	if retry < 0 {
		retry = 0
	}
	return QuotaResult{Allowed: allowed, Remaining: quota.maxBytes - entry.used, RetryAfter: retry}
}

func (quota *ByteQuota) maintainLocked(now time.Time, incomingKey string) {
	quota.operations++
	if quota.operations%1024 == 0 || len(quota.entries) >= quota.maxEntries {
		for key, entry := range quota.entries {
			if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= quota.window {
				delete(quota.entries, key)
			}
		}
	}
	if quota.maxEntries <= 0 || len(quota.entries) < quota.maxEntries {
		return
	}
	if _, exists := quota.entries[incomingKey]; exists {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, entry := range quota.entries {
		if oldestKey == "" || entry.windowStart.Before(oldest) {
			oldestKey = key
			oldest = entry.windowStart
		}
	}
	if oldestKey != "" {
		delete(quota.entries, oldestKey)
	}
}

// Used returns the charged bytes in the current window for key.
func (quota *ByteQuota) Used(key string) int64 {
	if quota.maxBytes <= 0 {
		return 0
	}
	quota.mu.Lock()
	defer quota.mu.Unlock()
	entry := quota.entries[key]
	if entry.windowStart.IsZero() || quota.now().Sub(entry.windowStart) >= quota.window {
		delete(quota.entries, key)
		return 0
	}
	return entry.used
}
