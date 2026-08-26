package buildinfo

import "testing"

func TestFormat(t *testing.T) {
	got := Format("netspeed", Info{
		Version: "v1.2.3",
		Commit:  "0123456789abcdef",
		Date:    "2026-08-25T17:00:00Z",
	})
	want := "netspeed v1.2.3 (commit: 0123456789abcdef, built: 2026-08-25T17:00:00Z)"
	if got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}

func TestFormatDefaultsEmptyMetadata(t *testing.T) {
	got := Format("netspeedd", Info{})
	want := "netspeedd dev (commit: unknown, built: unknown)"
	if got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}
