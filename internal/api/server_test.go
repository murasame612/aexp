package api

import (
	"context"
	"fmt"
	"testing"
)

func TestLogReadErrorKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "missing", err: fmt.Errorf("log file not found: /tmp/events.jsonl"), want: "file_missing"},
		{name: "unreachable", err: fmt.Errorf("resource mu is unreachable; cannot read remote log file events.jsonl"), want: "resource_unreachable"},
		{name: "timeout", err: context.DeadlineExceeded, want: "remote_timeout"},
		{name: "other", err: fmt.Errorf("ssh handshake failed"), want: "read_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logReadErrorKind(tt.err); got != tt.want {
				t.Fatalf("logReadErrorKind(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}
