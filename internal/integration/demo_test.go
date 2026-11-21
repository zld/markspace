// Package integration contains end-to-end tests that exercise the full
// local chain: in-process mock RTSP server, gortsplib client over both
// transports, and the project reorder/reassemble pipeline.
package integration

import (
	"context"
	"testing"
	"time"

	"rtspcheck/internal/demo"
	"rtspcheck/internal/rtsp"
	"rtspcheck/internal/transport"
)

const testTimeout = 10 * time.Second

func runLink(t *testing.T, tr string, fault rtsp.FaultMode) *transport.Summary {
	t.Helper()
	mock := rtsp.NewMockServer(fault, time.Millisecond)
	if err := mock.Start(); err != nil {
		t.Fatalf("mock start: %v", err)
	}
	defer mock.Close()

	manifest := demo.Generate(12, 41)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	streamDone := make(chan struct{})
	senderErr := make(chan error, 1)
	go func() {
		senderErr <- mock.Stream(ctx, manifest)
		close(streamDone)
	}()

	sum := transport.Run(ctx, mock.URL(), transport.Options{
		Transport:       tr,
		Timeout:         testTimeout,
		StreamDone:      streamDone,
		MaxPayload:      1400,
		KnownGaps:       manifest.ExpectedGaps,
		KnownDuplicates: manifest.ExpectedDuplicates,
	})

	cancel()
	select {
	case <-senderErr:
	case <-time.After(time.Second):
		t.Fatal("sender did not stop after cancellation")
	}
	return sum
}

func TestDemoEndToEnd(t *testing.T) {
	for _, tr := range []string{rtsp.TransportUDP, rtsp.TransportTCP} {
		t.Run(tr, func(t *testing.T) {
			sum := runLink(t, tr, rtsp.FaultNone)
			if sum.Err != nil {
				t.Fatalf("%s: unexpected error at stage %q: %v", tr, sum.Stage, sum.Err)
			}
			if sum.StateTransitions != 6 {
				t.Errorf("%s: state_transitions=%d want 6", tr, sum.StateTransitions)
			}
			if sum.RTPSeqGaps != 1 {
				t.Errorf("%s: rtp_seq_gaps=%d want 1", tr, sum.RTPSeqGaps)
			}
			if sum.FramesReassembled != 11 {
				t.Errorf("%s: frames_reassembled=%d want 11", tr, sum.FramesReassembled)
			}
			if sum.FramesDropped != 1 {
				t.Errorf("%s: frames_dropped=%d want 1", tr, sum.FramesDropped)
			}
			if sum.DuplicatePackets != 1 {
				t.Errorf("%s: duplicate_packets=%d want 1", tr, sum.DuplicatePackets)
			}
		})
	}
}

func TestBothTransportsProduceIdenticalCounts(t *testing.T) {
	udp := runLink(t, rtsp.TransportUDP, rtsp.FaultNone)
	tcp := runLink(t, rtsp.TransportTCP, rtsp.FaultNone)
	if udp.RTPSeqGaps != tcp.RTPSeqGaps ||
		udp.FramesReassembled != tcp.FramesReassembled ||
		udp.FramesDropped != tcp.FramesDropped ||
		udp.DuplicatePackets != tcp.DuplicatePackets ||
		udp.StateTransitions != tcp.StateTransitions {
		t.Fatalf("transport counts diverge:\nudp=%+v\ntcp=%+v", udp, tcp)
	}
}

func TestBadSDPFailsAtDescribe(t *testing.T) {
	sum := runLink(t, rtsp.TransportTCP, rtsp.FaultBadSDP)
	if sum.Err == nil {
		t.Fatal("expected a failure for invalid SDP, got success")
	}
	if sum.Stage != "describe" {
		t.Fatalf("want failure at describe stage, got %q", sum.Stage)
	}
}

func TestSetupRejectionFailsAtSetup(t *testing.T) {
	sum := runLink(t, rtsp.TransportUDP, rtsp.FaultSetupRejected)
	if sum.Err == nil {
		t.Fatal("expected a failure for rejected SETUP, got success")
	}
	if sum.Stage != "setup" {
		t.Fatalf("want failure at setup stage, got %q", sum.Stage)
	}
}
