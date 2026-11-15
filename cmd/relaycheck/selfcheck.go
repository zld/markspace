package main

import (
	"errors"
	"fmt"
	"time"

	"relaycheck/internal/fixture"
	"relaycheck/internal/metrics"
	"relaycheck/internal/relay"
	"relaycheck/internal/signaling"
	"relaycheck/internal/transport"
)

// cmdSelfCheck runs internal diagnostics that exercise the failure
// detectors: signaling rules, the loopback guard, sequence tracking, and
// jitter edge cases. It prints one PASS/FAIL line per check and exits 0
// when all pass, 5 otherwise.
func cmdSelfCheck() int {
	failed := 0
	check := func(name string, ok bool, detail string) {
		status := "PASS"
		if !ok {
			status = "FAIL"
			failed++
		}
		fmt.Printf("%s %s %s\n", status, name, detail)
	}

	// Signaling: unknown version is rejected.
	p := signaling.NewPeer("diag", 4)
	err := p.Deliver(signaling.Message{Version: 99, SessionID: "s", Seq: 1, Type: signaling.TypeOffer, Payload: "v=0"})
	check("signaling.unknown_version", errors.Is(err, signaling.ErrUnknownVersion), fmt.Sprint(err))

	// Signaling: duplicate sequence is rejected.
	p = signaling.NewPeer("diag", 4)
	_ = p.Deliver(signaling.Message{Version: 1, SessionID: "s", Seq: 1, Type: signaling.TypeOffer, Payload: "v=0"})
	err = p.Deliver(signaling.Message{Version: 1, SessionID: "s", Seq: 1, Type: signaling.TypeCandidate, Payload: "c"})
	check("signaling.duplicate_seq", errors.Is(err, signaling.ErrDuplicateSeq), fmt.Sprint(err))

	// Signaling: mismatched session is rejected.
	err = p.Deliver(signaling.Message{Version: 1, SessionID: "other", Seq: 2, Type: signaling.TypeCandidate, Payload: "c"})
	check("signaling.session_mismatch", errors.Is(err, signaling.ErrSessionMismatch), fmt.Sprint(err))

	// Signaling: answer before offer is an illegal transition.
	p2 := signaling.NewPeer("diag2", 4)
	err = p2.Deliver(signaling.Message{Version: 1, SessionID: "s", Seq: 1, Type: signaling.TypeAnswer, Payload: "v=0"})
	check("signaling.illegal_transition", errors.Is(err, signaling.ErrIllegalTransition), fmt.Sprint(err))

	// Transport: loopback guard accepts 127.0.0.1 and ::1, rejects others.
	check("transport.allow_ipv4_loopback", transport.ValidateLoopback("127.0.0.1") == nil, "")
	check("transport.allow_ipv6_loopback", transport.ValidateLoopback("::1") == nil, "")
	check("transport.reject_non_loopback", transport.ValidateLoopback("192.0.2.1") != nil, "")

	// Sequence tracker: duplicate and reorder detection.
	tr := relay.NewSequenceTracker()
	tr.Observe(10)
	tr.Observe(11)
	tr.Observe(13)
	dupEv := tr.Observe(11)   // retransmit
	reordEv := tr.Observe(12) // late first arrival
	check("relay.detect_duplicate", dupEv == relay.SeqDuplicate && tr.Duplicates() == 1, "")
	check("relay.detect_reorder", reordEv == relay.SeqReordered && tr.Reordered() == 1, "")

	// Jitter: timestamp wraparound stays finite and non-negative.
	j := metrics.NewJitter(90000)
	base := time.Now()
	j.Observe(0xFFFFFFFE, base)
	j.Observe(2, base.Add(20*time.Millisecond)) // wrapped timestamp
	j.Observe(4, base.Add(40*time.Millisecond))
	check("metrics.jitter_wraparound", j.Valid() && j.Milliseconds() >= 0, fmt.Sprintf("jitter_ms=%.3f", j.Milliseconds()))

	// Jitter: non-monotonic arrival clock is clamped, not negative.
	j2 := metrics.NewJitter(90000)
	j2.Observe(100, base)
	j2.Observe(200, base.Add(-time.Second)) // clock jumped backwards
	check("metrics.jitter_nonmonotonic", j2.Milliseconds() >= 0, fmt.Sprintf("jitter_ms=%.3f", j2.Milliseconds()))

	// Jitter: insufficient samples are flagged invalid.
	j3 := metrics.NewJitter(90000)
	j3.Observe(1, base)
	check("metrics.jitter_validity_flag", !j3.Valid() && j3.Samples() == 0, "")

	// Fixture: limits reject unbounded configurations.
	bad := fixture.Config{Count: fixture.MaxPackets + 1, PayloadBytes: 100, ClockRate: 90000}
	check("fixture.count_limit", bad.Validate() != nil, "")
	bad = fixture.Config{Count: 10, PayloadBytes: fixture.MaxPayloadBytes + 1, ClockRate: 90000}
	check("fixture.payload_limit", bad.Validate() != nil, "")

	if failed > 0 {
		fmt.Printf("self-check: %d check(s) failed\n", failed)
		return exitInternal
	}
	fmt.Println("self-check: all checks passed")
	return exitOK
}
