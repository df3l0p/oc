package harness

import "testing"

func TestNewReturnsRegisteredHarness(t *testing.T) {
	h, err := New("opencode", Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := h.(Opencode); !ok {
		t.Errorf("expected an Opencode harness, got %T", h)
	}
}

func TestNewSandboxReturnsSandboxHarness(t *testing.T) {
	h, err := New("opencode", Options{Sandbox: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s, ok := h.(*Sandbox)
	if !ok {
		t.Fatalf("expected a *Sandbox harness, got %T", h)
	}
	if s.image != DefaultSandboxImage {
		t.Errorf("image = %q, want default %q", s.image, DefaultSandboxImage)
	}
}

func TestNewUnknownHarnessListsAvailableNames(t *testing.T) {
	_, err := New("nope", Options{})
	if err == nil {
		t.Fatal("expected an error for an unregistered harness name")
	}
	const want = `unknown harness "nope" (available: opencode)`
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}
