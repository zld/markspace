package integration

import (
	"context"
	"errors"
	"testing"

	relaypkg "looprelay/internal/relay"
	rtcpkg "looprelay/internal/rtc"
	"looprelay/internal/signal"
)

func TestClassifyExit(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"nil":               {nil, ExitOK},
		"loopback":          {&rtcpkg.ErrNonLoopbackCandidate{Where: "x", Address: "10.0.0.1"}, ExitSignaling},
		"connect failed":    {rtcpkg.ErrConnectFailed, ExitSignaling},
		"connect timeout":   {rtcpkg.ErrConnectTimeout, ExitSignaling},
		"gathering timeout": {rtcpkg.ErrGatheringTimeout, ExitSignaling},
		"no pair":           {rtcpkg.ErrNoSelectedPair, ExitSignaling},
		"negotiation":       {signal.ErrNegotiationTimeout, ExitSignaling},
		"out of order":      {signal.ErrOutOfOrder, ExitSignaling},
		"duplicate":         {signal.ErrDuplicateRequest, ExitSignaling},
		"expired":           {signal.ErrExpiredRequest, ExitSignaling},
		"queue full":        {relaypkg.ErrQueueFull, ExitIntegrity},
		"write failed":      {relaypkg.ErrWriteFailed, ExitIntegrity},
		"count mismatch":    {ErrPacketCountMismatch, ExitIntegrity},
		"cancelled":         {context.Canceled, ExitInterrupted},
	}
	for name, tc := range cases {
		var err error = tc.err
		if tc.err != nil {
			err = errors.Join(tc.err)
		}
		if got := classifyExit(err); got != tc.want {
			t.Fatalf("%s: classifyExit=%d want %d", name, got, tc.want)
		}
	}
}
