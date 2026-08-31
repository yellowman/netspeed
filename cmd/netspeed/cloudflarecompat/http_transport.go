package cloudflarecompat

import (
	"bytes"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const cloudflareTransportProbeBytes = 64 * 1024

type cloudflareTransportSummary struct {
	CapabilitySource        string                       `json:"capabilitySource"`
	ProviderDefaultsOnly    bool                         `json:"providerDefaultsOnly"`
	QueryDiscriminatorsSent bool                         `json:"queryDiscriminatorsSent"`
	DownloadPath            string                       `json:"downloadPath"`
	UploadPath              string                       `json:"uploadPath"`
	LatencyPath             string                       `json:"latencyPath"`
	BytesParameter          string                       `json:"bytesParameter"`
	UploadPayload           string                       `json:"uploadPayload"`
	Selection               cloudflareTransportSelection `json:"selection"`
	AntiTransform           cloudflareAntiTransform      `json:"antiTransform"`
}

type cloudflareTransportSelection struct {
	DownloadPayloadRequested    string `json:"downloadPayloadRequested"`
	DownloadPayload             string `json:"downloadPayload"`
	DownloadPayloadEvidence     string `json:"downloadPayloadEvidence"`
	DownloadFramingRequested    string `json:"downloadFramingRequested"`
	DownloadFraming             string `json:"downloadFraming"`
	DownloadFramingEvidence     string `json:"downloadFramingEvidence"`
	DownloadChunkBytesRequested int    `json:"downloadChunkBytesRequested"`
	DownloadChunkBytes          *int   `json:"downloadChunkBytes,omitempty"`
	DownloadChunkBytesEvidence  string `json:"downloadChunkBytesEvidence,omitempty"`
	DownloadFlushRequested      string `json:"downloadFlushRequested"`
	DownloadFlush               *bool  `json:"downloadFlush,omitempty"`
	DownloadFlushEvidence       string `json:"downloadFlushEvidence,omitempty"`
}

type cloudflareAntiTransform struct {
	TransportCompressionDisabled   bool   `json:"transportCompressionDisabled"`
	RequestAcceptEncoding          string `json:"requestAcceptEncoding"`
	RequestCacheControl            string `json:"requestCacheControl"`
	RequestPragma                  string `json:"requestPragma"`
	UploadContentEncoding          string `json:"uploadContentEncoding"`
	ResponseContentEncoding        string `json:"responseContentEncoding"`
	ResponseNoStore                bool   `json:"responseNoStore"`
	ResponseNoTransform            bool   `json:"responseNoTransform"`
	ProxyBufferSuppressionObserved bool   `json:"proxyBufferSuppressionObserved"`
}

type transportControlError struct {
	message string
}

func (e *transportControlError) Error() string { return e.message }

func controlError(format string, args ...any) error {
	return &transportControlError{message: fmt.Sprintf(format, args...)}
}

func normalizedCloudflareTransportOptions(o options) (payload, framing, flush string, chunkBytes int) {
	payload = strings.ToLower(strings.TrimSpace(o.DownloadPayload))
	if payload == "" {
		payload = "auto"
	}
	framing = strings.ToLower(strings.TrimSpace(o.DownloadFraming))
	if framing == "" {
		framing = "auto"
	}
	flush = strings.ToLower(strings.TrimSpace(o.DownloadFlush))
	if flush == "" {
		flush = "auto"
	}
	return payload, framing, flush, o.DownloadChunkBytes
}

func probeAndNegotiateCloudflareTransport(ctx context.Context, client *http.Client, o options) (cloudflareTransportSummary, error) {
	payloadRequested, framingRequested, flushRequested, chunkRequested := normalizedCloudflareTransportOptions(o)
	summary := cloudflareTransportSummary{
		CapabilitySource:        "behavioral-probe",
		ProviderDefaultsOnly:    true,
		QueryDiscriminatorsSent: false,
		DownloadPath:            "/__down",
		UploadPath:              "/__up",
		LatencyPath:             "/__down",
		BytesParameter:          "bytes",
		UploadPayload:           "ascii-zero",
		Selection: cloudflareTransportSelection{
			DownloadPayloadRequested:    payloadRequested,
			DownloadFramingRequested:    framingRequested,
			DownloadChunkBytesRequested: chunkRequested,
			DownloadFlushRequested:      flushRequested,
		},
		AntiTransform: cloudflareAntiTransform{
			TransportCompressionDisabled: true,
			RequestAcceptEncoding:        "identity",
			RequestCacheControl:          "no-store, no-transform",
			RequestPragma:                "no-cache",
			UploadContentEncoding:        "identity",
		},
	}

	query := url.Values{
		"bytes": {strconv.Itoa(cloudflareTransportProbeBytes)},
		"id":    {strconv.FormatInt(cloudflareTransportProbeID(), 10)},
	}
	response, err := request(ctx, client, o, http.MethodGet, summary.DownloadPath, query, nil, -1)
	if err != nil {
		return summary, fmt.Errorf("probe Cloudflare download transport: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return summary, fmt.Errorf("probe Cloudflare download transport: HTTP %d", response.StatusCode)
	}
	if err := verifyCloudflareIdentityResponse(response); err != nil {
		return summary, fmt.Errorf("probe Cloudflare download transport: %w", err)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, cloudflareTransportProbeBytes+1))
	if err != nil {
		return summary, fmt.Errorf("read Cloudflare transport probe: %w", err)
	}
	if len(body) != cloudflareTransportProbeBytes {
		return summary, fmt.Errorf("Cloudflare transport probe returned %d of %d bytes", len(body), cloudflareTransportProbeBytes)
	}

	payload, payloadEvidence, err := inspectCloudflarePayload(response, body)
	if err != nil {
		return summary, err
	}
	framing, framingEvidence, err := inspectCloudflareFraming(response, cloudflareTransportProbeBytes)
	if err != nil {
		return summary, err
	}
	chunkBytes, chunkEvidence, err := inspectOptionalPositiveIntHeader(response.Header, "X-Netspeed-Chunk-Bytes")
	if err != nil {
		return summary, err
	}
	flush, flushEvidence, err := inspectOptionalBoolHeader(response.Header, "X-Netspeed-Flush")
	if err != nil {
		return summary, err
	}

	summary.Selection.DownloadPayload = payload
	summary.Selection.DownloadPayloadEvidence = payloadEvidence
	summary.Selection.DownloadFraming = framing
	summary.Selection.DownloadFramingEvidence = framingEvidence
	summary.Selection.DownloadChunkBytes = chunkBytes
	summary.Selection.DownloadChunkBytesEvidence = chunkEvidence
	summary.Selection.DownloadFlush = flush
	summary.Selection.DownloadFlushEvidence = flushEvidence

	encoding := strings.TrimSpace(response.Header.Get("Content-Encoding"))
	if encoding == "" {
		encoding = "identity"
	}
	cacheControl := strings.Join(response.Header.Values("Cache-Control"), ",")
	summary.AntiTransform.ResponseContentEncoding = strings.ToLower(encoding)
	summary.AntiTransform.ResponseNoStore = headerHasDirectiveCF(cacheControl, "no-store")
	summary.AntiTransform.ResponseNoTransform = headerHasDirectiveCF(cacheControl, "no-transform")
	summary.AntiTransform.ProxyBufferSuppressionObserved = strings.EqualFold(strings.TrimSpace(response.Header.Get("X-Accel-Buffering")), "no")

	if payloadRequested != "auto" && payloadRequested != payload {
		return summary, controlError("Cloudflare endpoint provider-default payload is %q (%s), so --download-payload=%s cannot be honored without sending a Netspeed-specific payload query parameter", payload, payloadEvidence, payloadRequested)
	}
	if framingRequested != "auto" && framingRequested != framing {
		return summary, controlError("Cloudflare endpoint provider-default framing is %q (%s), so --download-framing=%s cannot be honored without sending a Netspeed-specific framing query parameter", framing, framingEvidence, framingRequested)
	}
	if chunkRequested > 0 {
		if chunkBytes == nil {
			return summary, controlError("cannot verify --download-chunk-bytes=%d: the Cloudflare endpoint did not advertise an exact X-Netspeed-Chunk-Bytes value, and compatibility mode will not send an unrecognized chunkBytes query parameter", chunkRequested)
		}
		if *chunkBytes != chunkRequested {
			return summary, controlError("Cloudflare endpoint provider-default chunk size is %d, not the requested %d", *chunkBytes, chunkRequested)
		}
	}
	if flushRequested != "auto" {
		if flush == nil {
			return summary, controlError("cannot verify --download-flush=%s: the Cloudflare endpoint did not advertise X-Netspeed-Flush, and compatibility mode will not send an unrecognized flush query parameter", flushRequested)
		}
		requestedBool := flushRequested == "true"
		if *flush != requestedBool {
			return summary, controlError("Cloudflare endpoint provider-default flush setting is %t, not the requested %t", *flush, requestedBool)
		}
	}

	return summary, nil
}

func cloudflareTransportProbeID() int64 {
	return time.Now().UnixNano()
}

func inspectCloudflarePayload(response *http.Response, body []byte) (string, string, error) {
	classification, evidence := classifyCloudflarePayload(body)
	claimed := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Netspeed-Payload")))
	if claimed == "" {
		return classification, evidence, nil
	}
	if claimed != "random" && claimed != "zero" {
		return "", "", fmt.Errorf("Cloudflare-compatible download returned unsupported X-Netspeed-Payload %q", claimed)
	}
	if classification != claimed {
		return "", "", fmt.Errorf("Cloudflare-compatible download claimed payload %q but body inspection classified it as %q", claimed, classification)
	}
	return classification, "X-Netspeed-Payload=" + claimed + "; " + evidence, nil
}

func classifyCloudflarePayload(body []byte) (string, string) {
	if len(body) == 0 {
		return "empty", "zero-length-body"
	}
	allZero := true
	allASCIIZero := true
	var counts [256]int
	for _, value := range body {
		counts[value]++
		if value != 0 {
			allZero = false
		}
		if value != '0' {
			allASCIIZero = false
		}
	}
	if allZero {
		return "zero", "body-all-0x00"
	}
	if allASCIIZero {
		return "ascii-zero", "body-all-0x30"
	}

	entropy := 0.0
	for _, count := range counts {
		if count == 0 {
			continue
		}
		probability := float64(count) / float64(len(body))
		entropy -= probability * math.Log2(probability)
	}
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err == nil {
		_, _ = writer.Write(body)
		_ = writer.Close()
	}
	ratio := float64(compressed.Len()) / float64(len(body))
	evidence := fmt.Sprintf("body-entropy=%.3f-bits-per-byte,flate-ratio=%.3f", entropy, ratio)
	if len(body) >= 4096 && entropy >= 7.5 && ratio >= 0.95 {
		return "random", evidence
	}
	return "opaque", evidence
}

func inspectCloudflareFraming(response *http.Response, expectedBytes int) (string, string, error) {
	claimed := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Netspeed-Framing")))
	chunked := containsFoldCF(response.TransferEncoding, "chunked")
	if claimed != "" {
		switch claimed {
		case "fixed":
			if response.ContentLength != int64(expectedBytes) {
				return "", "", fmt.Errorf("download claimed fixed framing but Content-Length is %d, expected %d", response.ContentLength, expectedBytes)
			}
		case "chunked":
			if response.ContentLength >= 0 {
				return "", "", fmt.Errorf("download claimed chunked framing but supplied Content-Length %d", response.ContentLength)
			}
			if response.ProtoMajor == 1 && !chunked {
				return "", "", fmt.Errorf("download claimed chunked framing but HTTP/1.x transfer coding was not chunked")
			}
		default:
			return "", "", fmt.Errorf("Cloudflare-compatible download returned unsupported X-Netspeed-Framing %q", claimed)
		}
		return claimed, fmt.Sprintf("X-Netspeed-Framing=%s; protocol=%s", claimed, response.Proto), nil
	}

	if response.ContentLength >= 0 {
		if response.ContentLength != int64(expectedBytes) {
			return "", "", fmt.Errorf("download Content-Length is %d, expected %d", response.ContentLength, expectedBytes)
		}
		return "fixed", fmt.Sprintf("Content-Length=%d; protocol=%s", response.ContentLength, response.Proto), nil
	}
	if response.ProtoMajor == 1 && chunked {
		return "chunked", fmt.Sprintf("Transfer-Encoding=chunked; protocol=%s", response.Proto), nil
	}
	return "streamed", fmt.Sprintf("no-Content-Length; protocol=%s", response.Proto), nil
}

func inspectOptionalPositiveIntHeader(header http.Header, name string) (*int, string, error) {
	raw := strings.TrimSpace(header.Get(name))
	if raw == "" {
		return nil, "", nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return nil, "", fmt.Errorf("invalid %s %q", name, raw)
	}
	return &value, name + "=" + raw, nil
}

func inspectOptionalBoolHeader(header http.Header, name string) (*bool, string, error) {
	raw := strings.TrimSpace(header.Get(name))
	if raw == "" {
		return nil, "", nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, "", fmt.Errorf("invalid %s %q", name, raw)
	}
	return &value, name + "=" + strings.ToLower(raw), nil
}

func verifyCloudflareIdentityResponse(response *http.Response) error {
	if response.Uncompressed {
		return fmt.Errorf("measurement response was transparently decompressed by the HTTP transport")
	}
	encoding := strings.TrimSpace(response.Header.Get("Content-Encoding"))
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf("measurement response used unsupported Content-Encoding %q", encoding)
	}
	return nil
}

func verifyCloudflareDownloadSelection(response *http.Response, bodyPrefix []byte, expectedBytes int64, transport *cloudflareTransportSummary) error {
	if err := verifyCloudflareIdentityResponse(response); err != nil {
		return err
	}
	if response.ContentLength >= 0 && response.ContentLength != expectedBytes {
		return fmt.Errorf("download Content-Length %d; expected %d", response.ContentLength, expectedBytes)
	}
	if transport == nil || expectedBytes == 0 {
		return nil
	}
	cacheControl := strings.Join(response.Header.Values("Cache-Control"), ",")
	if transport.AntiTransform.ResponseNoStore && !headerHasDirectiveCF(cacheControl, "no-store") {
		return fmt.Errorf("download response lost the probed Cache-Control no-store directive")
	}
	if transport.AntiTransform.ResponseNoTransform && !headerHasDirectiveCF(cacheControl, "no-transform") {
		return fmt.Errorf("download response lost the probed Cache-Control no-transform directive")
	}
	if transport.AntiTransform.ProxyBufferSuppressionObserved && !strings.EqualFold(strings.TrimSpace(response.Header.Get("X-Accel-Buffering")), "no") {
		return fmt.Errorf("download response lost the probed X-Accel-Buffering: no evidence")
	}
	payload, _, err := inspectCloudflarePayload(response, bodyPrefix)
	if err != nil {
		return err
	}
	if payload != transport.Selection.DownloadPayload {
		return fmt.Errorf("download payload drifted from probed provider default %q to %q", transport.Selection.DownloadPayload, payload)
	}
	framing, _, err := inspectCloudflareFraming(response, int(expectedBytes))
	if err != nil {
		return err
	}
	if framing != transport.Selection.DownloadFraming {
		return fmt.Errorf("download framing drifted from probed provider default %q to %q", transport.Selection.DownloadFraming, framing)
	}
	if transport.Selection.DownloadChunkBytes != nil {
		actual, _, err := inspectOptionalPositiveIntHeader(response.Header, "X-Netspeed-Chunk-Bytes")
		if err != nil {
			return err
		}
		if actual == nil || *actual != *transport.Selection.DownloadChunkBytes {
			return fmt.Errorf("download chunk-size evidence drifted from probed value %d", *transport.Selection.DownloadChunkBytes)
		}
	}
	if transport.Selection.DownloadFlush != nil {
		actual, _, err := inspectOptionalBoolHeader(response.Header, "X-Netspeed-Flush")
		if err != nil {
			return err
		}
		if actual == nil || *actual != *transport.Selection.DownloadFlush {
			return fmt.Errorf("download flush evidence drifted from probed value %t", *transport.Selection.DownloadFlush)
		}
	}
	return nil
}

func headerHasDirectiveCF(value, target string) bool {
	for _, directive := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(directive), target) {
			return true
		}
	}
	return false
}

func containsFoldCF(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

type captureRange struct {
	start  int64
	end    int64
	bytes  []byte
	filled int
}

type distributedCapture struct {
	offset int64
	ranges []captureRange
}

func newDistributedCapture(totalBytes int64, budget int) *distributedCapture {
	if totalBytes <= 0 || budget <= 0 {
		return &distributedCapture{}
	}
	if totalBytes <= int64(budget) {
		return &distributedCapture{ranges: []captureRange{{
			start: 0,
			end:   totalBytes,
			bytes: make([]byte, int(totalBytes)),
		}}}
	}

	const windows = 4
	windowBytes := int64(budget / windows)
	if windowBytes < 1 {
		windowBytes = 1
	}
	maxStart := totalBytes - windowBytes
	ranges := make([]captureRange, 0, windows)
	for index := int64(0); index < windows; index++ {
		start := maxStart * index / (windows - 1)
		end := start + windowBytes
		ranges = append(ranges, captureRange{
			start: start,
			end:   end,
			bytes: make([]byte, int(windowBytes)),
		})
	}
	return &distributedCapture{ranges: ranges}
}

func (capture *distributedCapture) Write(data []byte) (int, error) {
	segmentStart := capture.offset
	segmentEnd := segmentStart + int64(len(data))
	for index := range capture.ranges {
		target := &capture.ranges[index]
		overlapStart := maxInt64(segmentStart, target.start)
		overlapEnd := minInt64(segmentEnd, target.end)
		if overlapStart >= overlapEnd {
			continue
		}
		sourceStart := overlapStart - segmentStart
		targetStart := overlapStart - target.start
		copied := copy(target.bytes[targetStart:targetStart+(overlapEnd-overlapStart)], data[sourceStart:sourceStart+(overlapEnd-overlapStart)])
		target.filled += copied
	}
	capture.offset = segmentEnd
	return len(data), nil
}

func (capture *distributedCapture) Bytes() ([]byte, error) {
	total := 0
	for _, target := range capture.ranges {
		if target.filled != len(target.bytes) {
			return nil, fmt.Errorf("download evidence capture filled %d of %d bytes for range [%d,%d)", target.filled, len(target.bytes), target.start, target.end)
		}
		total += len(target.bytes)
	}
	out := make([]byte, 0, total)
	for _, target := range capture.ranges {
		out = append(out, target.bytes...)
	}
	return out, nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
