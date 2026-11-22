package stream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"mediaqc/internal/timeline"
)

func makePackets(n, size int) []Packet {
	pkts := make([]Packet, n)
	for i := range pkts {
		pkts[i] = Packet{
			Track:   uint64(i%2 + 1),
			Ns:      int64(i) * 1_000_000,
			Offset:  int64(i * size),
			ReadOff: int64(i * size),
			ReadLen: int64(size),
		}
	}
	return pkts
}

func TestRunStreamsAllPackets(t *testing.T) {
	src := bytes.Repeat([]byte{0xAB}, 16*100)
	pkts := makePackets(16, 100)
	var got int
	stats, err := Run(context.Background(), bytes.NewReader(src), pkts,
		RunnerConfig{QueueCap: 4}, func(p Packet) error {
			got++
			if len(p.Data) != 100 || p.Data[0] != 0xAB {
				t.Errorf("packet %d payload wrong", got)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if got != 16 || stats.Packets != 16 {
		t.Fatalf("consumed %d of 16", got)
	}
	if stats.PeakQueueLen > 4 {
		t.Fatalf("peak queue %d exceeds cap 4", stats.PeakQueueLen)
	}
	if stats.PeakBufferBytes <= 0 {
		t.Fatalf("peak buffer bytes not tracked")
	}
	if len(stats.Latencies) != 16 {
		t.Fatalf("latencies = %d", len(stats.Latencies))
	}
}

func TestBackpressureBoundsMemory(t *testing.T) {
	// Slow consumer, small queue: peak buffered bytes must stay bounded
	// by queue capacity regardless of total stream size.
	src := bytes.Repeat([]byte{0x01}, 64*4096)
	pkts := makePackets(64, 4096)
	stats, err := Run(context.Background(), bytes.NewReader(src), pkts,
		RunnerConfig{QueueCap: 2, ConsumerDelay: time.Millisecond},
		func(p Packet) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if stats.PeakQueueLen > 2 {
		t.Fatalf("peak queue %d exceeds cap 2", stats.PeakQueueLen)
	}
	if stats.PeakBufferBytes > 3*4096 {
		t.Fatalf("peak buffer %d bytes not bounded by queue", stats.PeakBufferBytes)
	}
}

func TestCancelIsInterruptFault(t *testing.T) {
	src := bytes.Repeat([]byte{0x02}, 1000*100)
	pkts := makePackets(1000, 100)
	ctx, cancel := context.WithCancel(context.Background())
	consumed := 0
	_, err := Run(ctx, bytes.NewReader(src), pkts,
		RunnerConfig{QueueCap: 4}, func(p Packet) error {
			consumed++
			if consumed == 5 {
				cancel()
			}
			return nil
		})
	f := timeline.AsFault(err)
	if f == nil || f.Class != timeline.ClassInterrupted {
		t.Fatalf("want interrupted fault, got %v", err)
	}
	if f.Class.ExitCode() != 3 {
		t.Fatalf("interrupted exit code = %d, want 3", f.Class.ExitCode())
	}
	if consumed >= 1000 {
		t.Fatalf("cancellation did not stop the stream")
	}
}

func TestResumeValidation(t *testing.T) {
	sha := sha256.Sum256([]byte("input"))
	dir := t.TempDir()
	path := dir + "/out.qcstate"

	if a := DecideResume(true, nil, sha, "sum"); a != ResumeFresh {
		t.Fatalf("no state: got %s", a)
	}
	st := &State{InputSHA256: "deadbeef", IndexChecksum: "sum", Stage: StageInterrupted}
	if a := DecideResume(true, st, sha, "sum"); a != ResumeRescanned {
		t.Fatalf("digest mismatch: got %s", a)
	}
	st = &State{
		InputSHA256:   hexOf(sha),
		IndexChecksum: "sum",
		Stage:         StageInterrupted,
	}
	if err := SaveState(path, st); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil || loaded == nil {
		t.Fatalf("load: %v", err)
	}
	if a := DecideResume(true, loaded, sha, "sum"); a != ResumeResumed {
		t.Fatalf("valid state: got %s", a)
	}
	if a := DecideResume(true, loaded, sha, "other-sum"); a != ResumeRescanned {
		t.Fatalf("index checksum mismatch: got %s", a)
	}
	loaded.Stage = "bogus"
	if a := DecideResume(true, loaded, sha, "sum"); a != ResumeRescanned {
		t.Fatalf("stage invariant violation: got %s", a)
	}
	if a := DecideResume(false, loaded, sha, "sum"); a != ResumeFresh {
		t.Fatalf("resume not requested: got %s", a)
	}
}

func hexOf(s [32]byte) string {
	const hexdig = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range s {
		out[i*2] = hexdig[b>>4]
		out[i*2+1] = hexdig[b&0xF]
	}
	return string(out)
}
