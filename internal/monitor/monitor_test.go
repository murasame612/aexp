package monitor

import (
	"testing"
	"time"
)

func TestPollBackoffDelay(t *testing.T) {
	base := 3 * time.Second
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: 3 * time.Second},
		{failures: 1, want: 3 * time.Second},
		{failures: 2, want: 6 * time.Second},
		{failures: 3, want: 12 * time.Second},
		{failures: 4, want: 24 * time.Second},
		{failures: 5, want: 48 * time.Second},
		{failures: 6, want: 96 * time.Second},
		{failures: 7, want: 2 * time.Minute},
		{failures: 20, want: 2 * time.Minute},
	}
	for _, tt := range tests {
		if got := pollBackoffDelay(base, tt.failures); got != tt.want {
			t.Fatalf("pollBackoffDelay(%s, %d) = %s, want %s", base, tt.failures, got, tt.want)
		}
	}
}

func TestPollBackoffDelayUsesSafeBase(t *testing.T) {
	if got := pollBackoffDelay(0, 1); got != time.Second {
		t.Fatalf("zero base delay = %s, want 1s", got)
	}
}
