package channels

import (
	"strings"
	"testing"
)

func TestRetryStatusMessageUsesClassifiedCause(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   string
	}{
		{reason: "rate_limit", want: "rate limit"},
		{reason: "timeout", want: "timed out"},
		{reason: "server_error", want: "temporary error"},
		{reason: "overloaded", want: "overloaded"},
	} {
		got := retryStatusMessage(tc.reason, "2", "3")
		if !strings.Contains(strings.ToLower(got), tc.want) {
			t.Fatalf("reason %q produced %q, want %q", tc.reason, got, tc.want)
		}
	}
}
