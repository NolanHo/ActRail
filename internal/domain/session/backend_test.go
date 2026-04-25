package session

import "testing"

func TestParseBackendNormalizesCaseAndWhitespace(t *testing.T) {
	backend, err := ParseBackend("  PI  ")
	if err != nil {
		t.Fatalf("ParseBackend() error = %v", err)
	}
	if backend != BackendPI {
		t.Fatalf("backend = %q, want %q", backend, BackendPI)
	}
}

func TestParseBackendRejectsUnknownValue(t *testing.T) {
	if _, err := ParseBackend("anthropic"); err == nil {
		t.Fatal("ParseBackend() error = nil, want error")
	}
}
