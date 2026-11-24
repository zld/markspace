package demux_test

import (
	"math"
	"path/filepath"
	"testing"

	"mediapack/internal/demux"
	"mediapack/internal/fixture"
)

func TestDemuxRTPUnwrapsTimestampAndCountsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrap.rtp")
	const ssrc = 0xAABBCCDD
	start := uint32(math.MaxUint32 - 1000)
	packets := fixture.WrappedTimestampPackets(ssrc, 111, 12, start, 960)
	if err := fixture.WriteRTPFrames(path, packets); err != nil {
		t.Fatal(err)
	}
	doc, err := demux.DemuxRTP(path, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Streams) != 1 {
		t.Fatalf("streams=%d", len(doc.Streams))
	}
	st := doc.Streams[0]
	if st.Key.SSRC != ssrc || st.Key.PayloadType != 111 {
		t.Fatalf("key=%+v", st.Key)
	}
	if st.ReceivedPackets != 13 || st.DuplicatePackets != 1 {
		t.Fatalf("received=%d dup=%d", st.ReceivedPackets, st.DuplicatePackets)
	}
	if len(st.Samples) != 12 {
		t.Fatalf("samples=%d, duplicate was written", len(st.Samples))
	}
	wantLast := demux.UnwrapTimestamp(int64(start), start+11*960)
	if st.Samples[11].DTS != wantLast || st.Samples[11].DTS <= int64(math.MaxUint32) {
		t.Fatalf("unwrapped last=%d want=%d", st.Samples[11].DTS, wantLast)
	}
	if st.Samples[1].DTS <= st.Samples[0].DTS {
		t.Fatal("timestamps must advance")
	}
}

func TestDemuxRTPMissingPacketsAreGapsNotSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gap.rtp")
	if err := fixture.WriteRTPFrames(path, fixture.GapPackets(42, 96)); err != nil {
		t.Fatal(err)
	}
	doc, err := demux.DemuxRTP(path, 48000)
	if err != nil {
		t.Fatal(err)
	}
	st := doc.Streams[0]
	if len(st.Samples) != 4 {
		t.Fatalf("samples=%d", len(st.Samples))
	}
	if len(st.Gaps) != 1 || st.Gaps[0].MissingCount != 2 {
		t.Fatalf("gaps=%+v", st.Gaps)
	}
	if st.Gaps[0].FromSequence != 12 || st.Gaps[0].ToSequence != 13 {
		t.Fatalf("gap range=%d-%d", st.Gaps[0].FromSequence, st.Gaps[0].ToSequence)
	}
}

func TestUnwrapSequenceAcrossUint16Boundary(t *testing.T) {
	var prev *uint64
	want := []uint64{65534, 65535, 65536, 65537}
	for i, seq := range []uint16{65534, 65535, 0, 1} {
		got := demux.UnwrapSequence(prev, seq)
		if got != want[i] {
			t.Fatalf("seq %d: got %d want %d", seq, got, want[i])
		}
		prev = &got
	}
}
