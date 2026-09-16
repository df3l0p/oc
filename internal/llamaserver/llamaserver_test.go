package llamaserver

import "testing"

func TestOptionsBaseURL(t *testing.T) {
	opts := Options{Host: "127.0.0.1", Port: 8080}
	want := "http://127.0.0.1:8080"
	if got := opts.BaseURL(); got != want {
		t.Errorf("BaseURL() = %q, want %q", got, want)
	}
}

func TestIsHealthyFalseWhenNothingListening(t *testing.T) {
	if IsHealthy("http://127.0.0.1:1") {
		t.Error("expected IsHealthy to be false for an unreachable server")
	}
}
