package output

import "testing"

func TestDisplayVersion(t *testing.T) {
	tests := map[string]string{
		"":       "dev",
		"dev":    "dev",
		"1.2.3":  "v1.2.3",
		"v1.2.3": "v1.2.3",
	}
	for input, want := range tests {
		if got := displayVersion(input); got != want {
			t.Errorf("displayVersion(%q) = %q, want %q", input, got, want)
		}
	}
}
