package measurementhttp

import (
	"fmt"
	"net/http"
	"strings"
)

// ValidateIdentityResponseEncoding verifies every Content-Encoding field-value,
// including repeated header lines and comma-separated coding lists. A missing
// header means identity. Empty elements and any non-identity coding are rejected
// so a leading "identity" value cannot conceal a later gzip or Brotli value.
func ValidateIdentityResponseEncoding(header http.Header) error {
	for _, line := range header.Values("Content-Encoding") {
		for _, raw := range strings.Split(line, ",") {
			coding := strings.TrimSpace(raw)
			if coding == "" {
				return fmt.Errorf("measurement response has invalid empty Content-Encoding value")
			}
			if !strings.EqualFold(coding, "identity") {
				return fmt.Errorf("measurement response used unsupported Content-Encoding %q", coding)
			}
		}
	}
	return nil
}

// UniqueHeaderValue returns one normalized singleton field-value. Repeated
// identical lines are tolerated because some proxies duplicate response fields;
// conflicting lines or comma-joined values are rejected rather than trusting the
// first value returned by http.Header.Get.
func UniqueHeaderValue(header http.Header, name string) (string, bool, error) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", false, nil
	}
	var selected string
	for _, line := range values {
		if strings.Contains(line, ",") {
			return "", false, fmt.Errorf("measurement response has multiple %s values %q", name, strings.Join(values, ", "))
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return "", false, fmt.Errorf("measurement response has empty %s", name)
		}
		if selected == "" {
			selected = value
			continue
		}
		if !strings.EqualFold(selected, value) {
			return "", false, fmt.Errorf("measurement response has conflicting %s values %q", name, strings.Join(values, ", "))
		}
	}
	return selected, true, nil
}
