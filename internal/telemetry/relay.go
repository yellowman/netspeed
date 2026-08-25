// Package telemetry defines small cross-package operational snapshots.
package telemetry

// RelayStats is an immutable embedded-TURN UDP accounting snapshot.
type RelayStats struct {
	BytesRead          uint64
	BytesWritten       uint64
	PacketsRead        uint64
	PacketsWritten     uint64
	DroppedReadBytes   uint64
	RejectedWriteBytes uint64
}

// RelayStatsProvider supplies relay counters without coupling the HTTP server
// to the concrete TURN implementation.
type RelayStatsProvider interface {
	RelayStats() RelayStats
}
