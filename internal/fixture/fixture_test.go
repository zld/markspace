package fixture

import (
	"bytes"
	"math/rand"
	"testing"
)

func videoSpec(id uint32, count int, seed uint64) TrackSpec {
	tr := TrackSpec{ID: id, Handler: "vide", Timescale: 90000, Width: 320, Height: 180, Seed: seed}
	for i := 0; i < count; i++ {
		tr.Samples = append(tr.Samples, Sample{Delta: 3000, Size: uint32(40 + i%5), Sync: i%10 == 0})
	}
	return tr
}

func TestBuildMovieIsByteDeterministic(t *testing.T) {
	spec := MovieSpec{Timescale: 1000, Tracks: []TrackSpec{videoSpec(1, 40, 3)}}
	a, err := BuildMovie(spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildMovie(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("identical specs produced different bytes")
	}
}

func TestBuildMovieRejectsBadSpecs(t *testing.T) {
	cases := []MovieSpec{
		{Timescale: 0, Tracks: []TrackSpec{videoSpec(1, 2, 1)}},
		{Timescale: 1000},
		{Timescale: 1000, Brand: "toolong", Tracks: []TrackSpec{videoSpec(1, 2, 1)}},
		{Timescale: 1000, Tracks: []TrackSpec{{ID: 1, Handler: "vide", Timescale: 0, Samples: []Sample{{Delta: 1, Size: 1}}}}},
		{Timescale: 1000, Tracks: []TrackSpec{{ID: 1, Handler: "text", Timescale: 1000, Samples: []Sample{{Delta: 1, Size: 1}}}}},
		{Timescale: 1000, Tracks: []TrackSpec{{ID: 1, Handler: "vide", Timescale: 1000}}},
		{Timescale: 1000, Tracks: []TrackSpec{{ID: 1, Handler: "vide", Timescale: 1000, Samples: []Sample{{Delta: 1, Size: 0}}}}},
	}
	for i, spec := range cases {
		if _, err := BuildMovie(spec); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestBuildMovieAudioAndVideo(t *testing.T) {
	spec := MovieSpec{
		Brand:     "qt  ",
		Timescale: 1000,
		Tracks: []TrackSpec{
			videoSpec(1, 30, 5),
			{ID: 2, Handler: "soun", Timescale: 48000, Seed: 9, Samples: []Sample{
				{Delta: 1024, Size: 16}, {Delta: 1024, Size: 16},
			}},
		},
	}
	data, err := BuildMovie(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 100 {
		t.Fatalf("file too small: %d", len(data))
	}
	if !bytes.Contains(data, []byte("qt  ")) {
		t.Fatal("brand not present")
	}
	for _, box := range []string{"ftyp", "mdat", "moov", "trak", "stbl", "stco", "stsz"} {
		if !bytes.Contains(data, []byte(box)) {
			t.Errorf("missing box %q", box)
		}
	}
}

func TestPatchBoxUint32(t *testing.T) {
	data, err := BuildMovie(MovieSpec{Timescale: 1000, Tracks: []TrackSpec{videoSpec(1, 5, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	patched, err := PatchBoxUint32(data, "mdhd", 16, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(patched, data) {
		t.Fatal("patch changed nothing")
	}
	// Patching must not mutate the caller's slice.
	if bytes.Equal(data, patched[:len(data)]) {
		t.Fatal("input slice was mutated in place")
	}
	if _, err := PatchBoxUint32(data, "nope", 0, 1); err == nil {
		t.Fatal("missing box accepted")
	}
	if _, err := PatchBoxUint32(data, "mdhd", 1<<20, 1); err == nil {
		t.Fatal("out-of-range patch accepted")
	}
}

func TestContentSpecKeyframesAlignAcrossBitrates(t *testing.T) {
	spec := ContentSpec{
		DurationMS: 10000, SegmentMS: 2000, Bitrates: []int{400, 900, 1600},
		KeyframeMS: 2000, FrameRate: 30, AudioRate: 48000, AudioFrameMS: 20, Seed: 4,
	}
	specs := spec.VideoTrackSpecs()
	if len(specs) != 3 {
		t.Fatalf("specs = %d", len(specs))
	}
	var wantKeys []int
	for i, ms := range specs {
		var keys []int
		var tick int64
		for si, s := range ms.Tracks[0].Samples {
			if s.Sync {
				keys = append(keys, si)
				// Every keyframe must land on an exact 2s multiple.
				if tick%(2*90000) != 0 {
					t.Errorf("track %d keyframe %d at tick %d is not on a 2s boundary", i, si, tick)
				}
			}
			tick += int64(s.Delta)
		}
		if i == 0 {
			wantKeys = keys
			continue
		}
		if len(keys) != len(wantKeys) {
			t.Fatalf("track %d has %d keyframes, track 0 has %d", i, len(keys), len(wantKeys))
		}
		for j := range keys {
			if keys[j] != wantKeys[j] {
				t.Fatalf("track %d keyframe %d at sample %d, track 0 at %d", i, j, keys[j], wantKeys[j])
			}
		}
	}
	// Higher bitrates must produce strictly larger samples.
	small := specs[0].Tracks[0].Samples[0].Size
	big := specs[2].Tracks[0].Samples[0].Size
	if big <= small {
		t.Fatalf("1600kbps sample size %d not larger than 400kbps %d", big, small)
	}
}

func TestAudioTrackCoversDuration(t *testing.T) {
	spec := ContentSpec{DurationMS: 2000, Bitrates: []int{400}, AudioFrameMS: 20, Seed: 1}
	tr := spec.AudioTrackSpec()
	if tr.Handler != "soun" || tr.Timescale != 48000 {
		t.Fatalf("audio track = %+v", tr)
	}
	var ticks uint64
	for _, s := range tr.Samples {
		ticks += uint64(s.Delta)
	}
	if got := ticks * 1000 / 48000; got < 2000 {
		t.Fatalf("audio covers %d ms, want >= 2000", got)
	}
}

func TestSimulateNetworkAccountsEveryPacket(t *testing.T) {
	spec := NetworkSpec{
		Packets: 200, Loss: 0.05, JitterMS: 40, Reorder: 20, StartSeq: 65500,
		TimestampStep: 3000, Seed: 12,
	}
	trace := SimulateNetwork(spec)
	if len(trace.Packets)+len(trace.Dropped) != spec.Packets {
		t.Fatalf("delivered %d + dropped %d != %d",
			len(trace.Packets), len(trace.Dropped), spec.Packets)
	}
	if len(trace.Arrivals) != len(trace.Packets) || len(trace.Deadlines) != len(trace.Packets) {
		t.Fatal("arrival/deadline slices misaligned")
	}
	// Arrivals must be non-decreasing: the trace is in arrival order.
	for i := 1; i < len(trace.Arrivals); i++ {
		if trace.Arrivals[i] < trace.Arrivals[i-1] {
			t.Fatalf("arrival %d (%d) before %d (%d)",
				i, trace.Arrivals[i], i-1, trace.Arrivals[i-1])
		}
	}
	// The trace must not be in sequence order, or jitter is not being modelled.
	inOrder := true
	for i := 1; i < len(trace.Packets); i++ {
		if trace.Packets[i].SequenceNumber < trace.Packets[i-1].SequenceNumber &&
			trace.Packets[i-1].SequenceNumber-trace.Packets[i].SequenceNumber < 1000 {
			inOrder = false
			break
		}
	}
	if inOrder {
		t.Fatal("network trace is in sequence order; reordering was not simulated")
	}
}

func TestSimulateNetworkDeterministic(t *testing.T) {
	spec := NetworkSpec{Packets: 80, Loss: 0.1, JitterMS: 50, Reorder: 10, Seed: 3}
	a := SimulateNetwork(spec)
	b := SimulateNetwork(spec)
	if len(a.Packets) != len(b.Packets) {
		t.Fatalf("packet count differs: %d vs %d", len(a.Packets), len(b.Packets))
	}
	for i := range a.Packets {
		if a.Packets[i].SequenceNumber != b.Packets[i].SequenceNumber || a.Arrivals[i] != b.Arrivals[i] {
			t.Fatalf("trace differs at %d", i)
		}
	}
}

func TestSimulateNetworkBurst(t *testing.T) {
	trace := SimulateNetwork(NetworkSpec{Packets: 100, BurstLoss: 7, BurstAt: 20, JitterMS: 5, Seed: 2})
	if len(trace.Dropped) < 7 {
		t.Fatalf("dropped = %d, want at least the 7-packet burst", len(trace.Dropped))
	}
	seen := make(map[uint16]bool, len(trace.Dropped))
	for _, seq := range trace.Dropped {
		seen[seq] = true
	}
	for seq := uint16(20); seq < 27; seq++ {
		if !seen[seq] {
			t.Errorf("burst sequence %d was not dropped", seq)
		}
	}
}

func TestSimulateNetworkTotalLoss(t *testing.T) {
	trace := SimulateNetwork(NetworkSpec{Packets: 10, Loss: 1, Seed: 1})
	if len(trace.Packets) != 0 {
		t.Fatalf("delivered %d packets on a total-loss channel", len(trace.Packets))
	}
	if len(trace.Dropped) != 10 {
		t.Fatalf("dropped = %d", len(trace.Dropped))
	}
}

func TestRepresentationName(t *testing.T) {
	cases := map[int]string{0: "0kbps", 7: "7kbps", 400: "400kbps", 1600: "1600kbps"}
	for in, want := range cases {
		if got := RepresentationName(in); got != want {
			t.Errorf("RepresentationName(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestRNGSeededIndependentlyOfPackageRand(t *testing.T) {
	// Fixture output must depend only on the per-track seed, never on the
	// package-level rand source, which other code may have consumed.
	rand.Int63() // advance the global source
	a, _ := BuildMovie(MovieSpec{Timescale: 1000, Tracks: []TrackSpec{videoSpec(1, 10, 42)}})
	rand.Int63()
	rand.Int63()
	b, _ := BuildMovie(MovieSpec{Timescale: 1000, Tracks: []TrackSpec{videoSpec(1, 10, 42)}})
	if !bytes.Equal(a, b) {
		t.Fatal("fixture output depends on global rand state")
	}
}
