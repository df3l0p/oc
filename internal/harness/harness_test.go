package harness

import "testing"

func TestNewReturnsRegisteredHarness(t *testing.T) {
	h, err := New("opencode")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := h.(Opencode); !ok {
		t.Errorf("expected an Opencode harness, got %T", h)
	}
}

func TestNewUnknownHarnessListsAvailableNames(t *testing.T) {
	_, err := New("nope")
	if err == nil {
		t.Fatal("expected an error for an unregistered harness name")
	}
	const want = `unknown harness "nope" (available: opencode)`
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}
