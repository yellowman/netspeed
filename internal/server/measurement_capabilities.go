package server

import (
	"github.com/yellowman/netspeed/internal/measurementhttp"
	"github.com/yellowman/netspeed/internal/meta"
)

func advertisedMeasurementCapabilities() *meta.MeasurementCapabilities {
	return &meta.MeasurementCapabilities{
		Version:                       measurementhttp.TransportVersion,
		DownloadPath:                  "/__down",
		DownloadBytesParameter:        "bytes",
		DownloadPayloadParameter:      "payload",
		DownloadFramingParameter:      "framing",
		DownloadChunkBytesParameter:   "chunkBytes",
		DownloadFlushParameter:        "flush",
		UploadPath:                    "/__up",
		UploadBytesParameter:          "bytes",
		HTTPPingPath:                  "/__ping",
		HTTPPingMethods:               []string{"GET", "HEAD"},
		WebSocketPingPath:             "/__ws",
		WebSocketPingProtocol:         measurementhttp.WebSocketPingSubprotocol,
		WebSocketPingPayloadBytes:     measurementhttp.WebSocketPingPayloadBytes,
		WarmConnectionPing:            true,
		DownloadPayloads:              []string{string(measurementhttp.PayloadRandom), string(measurementhttp.PayloadZero)},
		DownloadFramings:              []string{string(measurementhttp.FramingFixed), string(measurementhttp.FramingChunked)},
		DefaultDownloadPayload:        string(measurementhttp.PayloadRandom),
		DefaultDownloadFraming:        string(measurementhttp.FramingFixed),
		DefaultChunkBytes:             measurementhttp.DefaultChunkBytes,
		MinimumChunkBytes:             measurementhttp.MinChunkBytes,
		MaximumChunkBytes:             measurementhttp.MaxChunkBytes,
		UploadContentEncodings:        []string{"identity"},
		ResponseCacheControl:          measurementhttp.CacheControl,
		NoTransform:                   true,
		ProxyBufferSuppressionHeader:  "X-Accel-Buffering: no",
		ProxyRequestBufferingAdvisory: true,
	}
}
