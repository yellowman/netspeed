package limits

import (
	"sync"
	"time"
)

type quotaEntry struct {
	windowStart time.Time
	committed   int64
	reserved    int64
	id          uint64
}

func (entry quotaEntry) remaining(maxBytes int64) int64 {
	remaining := maxBytes - entry.committed
	if entry.reserved >= remaining {
		return 0
	}
	return remaining - entry.reserved
}

func (entry *quotaEntry) commit(n, maxBytes int64) {
	// Consumption never decreases. Saturate committed bytes independently of
	// pending writes so settlement cannot erase a concurrent over-limit read.
	if n >= maxBytes-entry.committed {
		entry.committed = maxBytes
	} else {
		entry.committed += n
	}
}

// ByteQuota is a fixed-window per-key byte quota. Whole-transfer reservations
// retain their charge on disconnect; ReserveWrite instead settles actual I/O.
type ByteQuota struct {
	mu          sync.Mutex
	maxBytes    int64
	window      time.Duration
	now         func() time.Time
	entries     map[string]quotaEntry
	operations  uint64
	maxEntries  int
	nextEntryID uint64
}

// QuotaResult describes one reservation attempt.
type QuotaResult struct {
	Allowed    bool
	Remaining  int64
	RetryAfter time.Duration
	entryID    uint64
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
	return quota.reserve(key, n, false)
}

func (quota *ByteQuota) reserve(key string, n int64, pendingWrite bool) QuotaResult {
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
		quota.nextEntryID++
		entry = quotaEntry{windowStart: now, id: quota.nextEntryID}
	}

	remaining := entry.remaining(quota.maxBytes)
	if n > remaining {
		retry := quota.window - now.Sub(entry.windowStart)
		if retry < 0 {
			retry = 0
		}
		quota.entries[key] = entry
		return QuotaResult{
			Allowed:    false,
			Remaining:  remaining,
			RetryAfter: retry,
		}
	}

	if pendingWrite {
		entry.reserved += n
	} else {
		entry.commit(n, quota.maxBytes)
	}
	quota.entries[key] = entry
	return QuotaResult{
		Allowed:    true,
		Remaining:  entry.remaining(quota.maxBytes),
		RetryAfter: quota.window - now.Sub(entry.windowStart),
		entryID:    entry.id,
	}
}

// ReserveWrite admits a bounded write atomically, then settles its actual byte
// count. Pending writes are separate from committed traffic; settlement releases
// only its own reservation and commits the written bytes. Whole-transfer
// reservations remain committed even when a transfer fails.
// Settlement is idempotent and cannot credit a newer or recreated quota entry.
func (quota *ByteQuota) ReserveWrite(key string, n int64) (QuotaResult, func(int64)) {
	result := quota.reserve(key, n, true)
	var once sync.Once
	return result, func(written int64) {
		once.Do(func() {
			if !result.Allowed || quota.maxBytes <= 0 || n == 0 {
				return
			}
			if written < 0 {
				written = 0
			} else if written > n {
				written = n
			}
			quota.mu.Lock()
			defer quota.mu.Unlock()
			entry, exists := quota.entries[key]
			if !exists || entry.id != result.entryID {
				return
			}
			entry.reserved -= n
			entry.commit(written, quota.maxBytes)
			quota.entries[key] = entry
		})
	}
}

// Charge accounts actual bytes already consumed from a stream. When n crosses
// the allowance after pending writes, Allowed is false, but all consumption is
// still committed independently of those reservations. This differs from
// Reserve, which is all-or-nothing for a known future transfer.
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
		quota.nextEntryID++
		entry = quotaEntry{windowStart: now, id: quota.nextEntryID}
	}
	remaining := entry.remaining(quota.maxBytes)
	allowed := n <= remaining
	entry.commit(n, quota.maxBytes)
	quota.entries[key] = entry
	retry := quota.window - now.Sub(entry.windowStart)
	if retry < 0 {
		retry = 0
	}
	return QuotaResult{Allowed: allowed, Remaining: entry.remaining(quota.maxBytes), RetryAfter: retry}
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

// Used returns committed bytes plus pending writes in the current window for
// key, capped at the quota ceiling. It is an enforcement counter, not telemetry
// for traffic consumed beyond that ceiling.
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
	return quota.maxBytes - entry.remaining(quota.maxBytes)
}
