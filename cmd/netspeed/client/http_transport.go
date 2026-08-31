package client

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yellowman/netspeed/internal/measurementhttp"
)

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values)+5)
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}
	return cloned
}

func (c *Client) downloadMeasurementURL(numBytes int64, values url.Values) string {
	selection := c.measurementTransport
	query := cloneValues(values)
	bytesKey := selection.DownloadBytesKey
	if bytesKey == "" {
		bytesKey = "bytes"
	}
	query.Set(bytesKey, strconv.FormatInt(numBytes, 10))
	if !selection.LegacyFallback {
		query.Set(selection.DownloadPayloadKey, string(selection.DownloadPayload))
		query.Set(selection.DownloadFramingKey, string(selection.DownloadFraming))
		query.Set(selection.DownloadChunkKey, strconv.Itoa(selection.DownloadChunkBytes))
		query.Set(selection.DownloadFlushKey, strconv.FormatBool(selection.DownloadFlush))
	}
	return buildMeasurementURL(c.cfg.ServerURL, selection.DownloadPath, query)
}

func (c *Client) uploadMeasurementURL(numBytes int64, values url.Values) string {
	selection := c.measurementTransport
	query := cloneValues(values)
	if selection.UploadBytesKey != "" {
		query.Set(selection.UploadBytesKey, strconv.FormatInt(numBytes, 10))
	}
	return buildMeasurementURL(c.cfg.ServerURL, selection.UploadPath, query)
}

func (c *Client) latencyMeasurementRequest(values url.Values) (string, string) {
	selection := c.measurementTransport
	if selection.LatencyUsesDownload {
		return selection.LatencyMethod, c.downloadMeasurementURL(0, values)
	}
	return selection.LatencyMethod, buildMeasurementURL(c.cfg.ServerURL, selection.LatencyPath, cloneValues(values))
}

func (c *Client) setMeasurementRequestHeaders(request *http.Request) {
	c.setRequestHeaders(request)
	request.Header.Set("Cache-Control", measurementhttp.CacheControl)
	request.Header.Set("Pragma", "no-cache")
}

func (c *Client) verifyCommonMeasurementResponse(response *http.Response, expectedMeasurement string) error {
	if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf("measurement response used unsupported Content-Encoding %q", encoding)
	}
	if c.measurementTransport.LegacyFallback {
		return nil
	}
	cacheControl := strings.Join(response.Header.Values("Cache-Control"), ",")
	if !headerHasDirective(cacheControl, "no-store") || !headerHasDirective(cacheControl, "no-transform") {
		return fmt.Errorf("measurement response Cache-Control %q does not preserve no-store, no-transform", cacheControl)
	}
	if got := strings.TrimSpace(response.Header.Get("X-Netspeed-Measurement")); !strings.EqualFold(got, expectedMeasurement) {
		return fmt.Errorf("measurement response type %q; expected %q", got, expectedMeasurement)
	}
	return nil
}

func (c *Client) verifyDownloadMeasurementResponse(response *http.Response, expectedBytes int64, expectedMeasurement string) error {
	if err := c.verifyCommonMeasurementResponse(response, expectedMeasurement); err != nil {
		return err
	}
	if c.measurementTransport.LegacyFallback {
		return nil
	}
	selection := c.measurementTransport
	if got := strings.TrimSpace(response.Header.Get("X-Netspeed-Payload")); !strings.EqualFold(got, string(selection.DownloadPayload)) {
		return fmt.Errorf("download response payload %q; expected %q", got, selection.DownloadPayload)
	}
	if got := strings.TrimSpace(response.Header.Get("X-Netspeed-Framing")); !strings.EqualFold(got, string(selection.DownloadFraming)) {
		return fmt.Errorf("download response framing %q; expected %q", got, selection.DownloadFraming)
	}
	chunkBytes, err := parseRequiredInt64Header(response.Header, "X-Netspeed-Chunk-Bytes")
	if err != nil {
		return err
	}
	if chunkBytes != int64(selection.DownloadChunkBytes) {
		return fmt.Errorf("download response chunk size %d; expected %d", chunkBytes, selection.DownloadChunkBytes)
	}
	if got := strings.TrimSpace(response.Header.Get("X-Netspeed-Flush")); !strings.EqualFold(got, strconv.FormatBool(selection.DownloadFlush)) {
		return fmt.Errorf("download response flush %q; expected %t", got, selection.DownloadFlush)
	}
	switch selection.DownloadFraming {
	case measurementhttp.FramingFixed:
		if response.ContentLength != expectedBytes {
			return fmt.Errorf("fixed download Content-Length %d; expected %d", response.ContentLength, expectedBytes)
		}
	case measurementhttp.FramingChunked:
		if response.ContentLength >= 0 {
			return fmt.Errorf("streamed download unexpectedly supplied Content-Length %d", response.ContentLength)
		}
		if response.ProtoMajor == 1 && !containsFold(response.TransferEncoding, "chunked") {
			return fmt.Errorf("HTTP/1.x streamed download did not use chunked transfer coding")
		}
	default:
		return fmt.Errorf("unsupported negotiated download framing %q", selection.DownloadFraming)
	}
	return nil
}

func (c *Client) verifyDedicatedLatencyResponse(response *http.Response) error {
	if err := c.verifyCommonMeasurementResponse(response, "latency"); err != nil {
		return err
	}
	if response.ContentLength >= 0 && response.ContentLength != 0 {
		return fmt.Errorf("latency response Content-Length %d; expected 0", response.ContentLength)
	}
	return nil
}

func (c *Client) verifyUploadMeasurementResponse(response *http.Response, expectedBytes int64) error {
	if err := c.verifyCommonMeasurementResponse(response, "upload"); err != nil {
		return err
	}
	if c.measurementTransport.LegacyFallback {
		return nil
	}
	for header, expected := range map[string]string{
		"X-Netspeed-Payload":          "discarded",
		"X-Netspeed-Framing":          "fixed",
		"X-Netspeed-Content-Encoding": "identity",
	} {
		if got := strings.TrimSpace(response.Header.Get(header)); !strings.EqualFold(got, expected) {
			return fmt.Errorf("upload response %s %q; expected %q", header, got, expected)
		}
	}
	if c.measurementTransport.UploadBytesKey != "" {
		declared, err := parseRequiredInt64Header(response.Header, "X-Netspeed-Expected-Bytes")
		if err != nil {
			return err
		}
		if declared != expectedBytes {
			return fmt.Errorf("upload response expected-byte count %d; expected %d", declared, expectedBytes)
		}
	}
	accepted, err := parseRequiredInt64Header(response.Header, "X-Netspeed-Accepted-Bytes")
	if err != nil {
		return err
	}
	if accepted != expectedBytes {
		return fmt.Errorf("upload response accepted-byte count %d; expected %d", accepted, expectedBytes)
	}
	if duration, err := parseRequiredInt64Header(response.Header, "X-Netspeed-Upload-Duration-Ns"); err != nil || duration <= 0 {
		if err != nil {
			return err
		}
		return fmt.Errorf("upload response duration %d is not positive", duration)
	}
	return nil
}

func parseRequiredInt64Header(header http.Header, name string) (int64, error) {
	raw := strings.TrimSpace(header.Get(name))
	if raw == "" {
		return 0, fmt.Errorf("measurement response is missing %s", name)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("measurement response has invalid %s %q", name, raw)
	}
	return value, nil
}

func headerHasDirective(value, target string) bool {
	for _, directive := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(directive), target) {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
