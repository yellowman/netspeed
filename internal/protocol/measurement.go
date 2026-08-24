// Package protocol defines the wire contract shared by netspeedd and its clients.
package protocol

const (
	// MeasurementProtocolVersion identifies the measurement capability contract
	// advertised by /meta.
	MeasurementProtocolVersion = 1

	// UploadReceiptVersion identifies the verified upload receipt schema.
	UploadReceiptVersion = 1
)

// UploadReceipt is returned by POST /__up after the server has consumed the
// complete request body. Clients must verify AcceptedBytes before accepting an
// upload sample.
type UploadReceipt struct {
	OK               bool  `json:"ok"`
	AcceptedBytes    int64 `json:"acceptedBytes"`
	ServerDurationNS int64 `json:"serverDurationNs"`
}
