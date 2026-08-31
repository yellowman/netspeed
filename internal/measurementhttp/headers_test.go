package measurementhttp

import (
	"net/http"
	"strings"
	"testing"
)

func TestValidateIdentityResponseEncodingChecksEveryValue(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		wantErr bool
	}{
		{name: "missing"},
		{name: "identity", values: []string{"identity"}},
		{name: "repeated identity", values: []string{"identity", "IDENTITY"}},
		{name: "comma identity", values: []string{"identity, identity"}},
		{name: "later gzip", values: []string{"identity", "gzip"}, wantErr: true},
		{name: "comma brotli", values: []string{"identity, br"}, wantErr: true},
		{name: "empty element", values: []string{"identity,"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := make(http.Header)
			for _, value := range test.values {
				header.Add("Content-Encoding", value)
			}
			err := ValidateIdentityResponseEncoding(header)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateIdentityResponseEncoding() error = %v; wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestUniqueHeaderValueRejectsConflicts(t *testing.T) {
	header := make(http.Header)
	header.Add("X-Netspeed-Flush", "true")
	header.Add("X-Netspeed-Flush", "false")
	_, _, err := UniqueHeaderValue(header, "X-Netspeed-Flush")
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("UniqueHeaderValue() error = %v; want conflicting-values error", err)
	}

	header = make(http.Header)
	header.Add("X-Accel-Buffering", "no")
	header.Add("X-Accel-Buffering", "NO")
	value, present, err := UniqueHeaderValue(header, "X-Accel-Buffering")
	if err != nil || !present || value != "no" {
		t.Fatalf("UniqueHeaderValue() = %q, %v, %v; want no, true, nil", value, present, err)
	}
}
