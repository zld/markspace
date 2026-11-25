package stream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pion/rtp"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
)

func TestBoundedQueueSlowConsumerAppliesBackpressureWithoutLoss(t *testing.T) {
	q := NewQueue("1080p-slow", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	first := Frame{Sequence: 1, Segment: 0, Boundary: true}
	second := Frame{Sequence: 2, Segment: 1, Boundary: true}
	if err := q.Enqueue(ctx, first); err != nil {
		t.Fatal(err)
	}

	returned := make(chan error, 1)
	go func() { returned <- q.Enqueue(ctx, second) }()
	select {
	case err := <-returned:
		t.Fatalf("second enqueue returned early (%v); expected backpressure", err)
	case <-time.After(50 * time.Millisecond):
	}

	got, err := q.Dequeue(ctx)
	if err != nil || got.Sequence != 1 || !got.Boundary {
		t.Fatalf("dequeue first=%+v err=%v", got, err)
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("second enqueue failed after drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backpressure did not release after drain")
	}

	got2, err := q.Dequeue(ctx)
	if err != nil || got2.Sequence != 2 || !got2.Boundary {
		t.Fatalf("boundary lost: second=%+v err=%v", got2, err)
	}
}

func TestFailedLayerDoesNotBlockHealthyLayer(t *testing.T) {
	f := NewFanout([]string{"healthy", "corrupt"}, 2)
	bad := f.Queue("corrupt")
	bad.Fail(fault.New(fault.CorruptFrame, "injected corruption"))

	errs := f.Broadcast(context.Background(), Frame{Sequence: 7})
	if len(errs) != 0 {
		t.Fatalf("failed layer should be skipped, got %v", errs)
	}
	got, err := f.Queue("healthy").Dequeue(context.Background())
	if err != nil || got.Sequence != 7 {
		t.Fatalf("healthy layer frame=%+v err=%v", got, err)
	}
	if bad.Failed() == nil || fault.Classify(bad.Failed()) != fault.CorruptFrame {
		t.Fatalf("bad layer state=%v", bad.Failed())
	}
}

func TestDuplicateRTPSequenceIsLocated(t *testing.T) {
	pkts := []rtp.Packet{
		{Header: rtp.Header{SequenceNumber: 10, Timestamp: 1000}},
		{Header: rtp.Header{SequenceNumber: 11, Timestamp: 2000}},
		{Header: rtp.Header{SequenceNumber: 11, Timestamp: 3000}},
	}
	err := ValidateRTPPackets("720p", 3, pkts)
	if fault.Classify(err) != fault.DuplicateSequence {
		t.Fatalf("kind=%q err=%v", fault.Classify(err), err)
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Layer != "720p" || fe.Segment == nil || *fe.Segment != 3 {
		t.Fatalf("location=%+v", fe)
	}
}

func TestRTPTimestampRegressionClassified(t *testing.T) {
	pkts := []rtp.Packet{
		{Header: rtp.Header{SequenceNumber: 1, Timestamp: 90000}},
		{Header: rtp.Header{SequenceNumber: 2, Timestamp: 80000}},
	}
	err := ValidateRTPPackets("480p", 2, pkts)
	if fault.Classify(err) != fault.TimestampRegression {
		t.Fatalf("kind=%q err=%v", fault.Classify(err), err)
	}
}

func TestVerifyBoundaryAndMissingSegment(t *testing.T) {
	frame := clock.Rate2997ND.FrameDuration()
	windows := []Window{{Index: 0, Start: 0, End: 2 * frame}, {Index: 1, Start: 2 * frame, End: 4 * frame}}
	good := []Segment{
		{Layer: "a", Index: 0, Start: 0, End: 2 * frame},
		{Layer: "a", Index: 1, Start: 2 * frame, End: 4 * frame},
		{Layer: "b", Index: 0, Start: 0, End: 2 * frame},
	}
	_, err := Verify([]string{"a", "b"}, windows, good, frame)
	if fault.Classify(err) != fault.MissingSegment {
		t.Fatalf("missing kind=%q err=%v", fault.Classify(err), err)
	}

	bad := append(good, Segment{Layer: "b", Index: 1, Start: 2*frame + 3*frame, End: 4 * frame})
	_, err = Verify([]string{"a", "b"}, windows, bad, frame)
	if fault.Classify(err) != fault.BoundaryMismatch {
		t.Fatalf("boundary kind=%q err=%v", fault.Classify(err), err)
	}
	var fe *fault.Error
	if errors.As(err, &fe) && (fe.Layer != "b" || fe.Segment == nil || *fe.Segment != 2) {
		t.Fatalf("boundary location=%+v want b segment 2", fe)
	}

	aligned := []Segment{
		{Layer: "a", Index: 0, Start: 0, End: 2*frame + 10},
		{Layer: "a", Index: 1, Start: 2 * frame, End: 4 * frame},
		{Layer: "b", Index: 0, Start: 0, End: 2 * frame},
		{Layer: "b", Index: 1, Start: 2 * frame, End: 4 * frame},
	}
	res, err := Verify([]string{"a", "b"}, windows, aligned, frame)
	if err != nil {
		t.Fatalf("sub-frame error should pass: %v", err)
	}
	if res.AlignmentErrorMS > frame.Milliseconds() {
		t.Fatalf("alignment %.3f > frame %.3f", res.AlignmentErrorMS, frame.Milliseconds())
	}
}
