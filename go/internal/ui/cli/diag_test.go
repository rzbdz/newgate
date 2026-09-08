package cli

import "testing"

func TestCIDRsOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"10.0.30.0/23", "10.0.30.0/23", true},
		{"10.0.30.0/23", "10.0.30.51/32", true},
		{"10.0.30.0/23", "10.0.32.0/24", false},
		{"bad", "10.0.30.0/23", false},
	}
	for _, tt := range tests {
		if got := cidrsOverlap(tt.a, tt.b); got != tt.want {
			t.Errorf("cidrsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestIndentLinesDoesNotTruncate(t *testing.T) {
	got := indentLines("first line\nfull second-line error", "  ")
	want := "first line\n  full second-line error"
	if got != want {
		t.Fatalf("indentLines() = %q, want %q", got, want)
	}
}
