package container

import (
	"bytes"
	"encoding/binary"
	"testing"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/media"
)

// videoTrack builds a 30fps track at 90kHz with a keyframe every 30 samples.
func videoTrack(id uint32, timescale uint32, count int, seed uint64) fixture.TrackSpec {
	tr := fixture.TrackSpec{ID: id, Handler: "vide", Timescale: timescale, Width: 320, Height: 180, Seed: seed}
	for i := 0; i < count; i++ {
		tr.Samples = append(tr.Samples, fixture.Sample{
			Delta: uint32(timescale / 30),
			Size:  uint32(64 + i%7),
			Sync:  i%30 == 0,
		})
	}
	return tr
}

func audioTrack(id uint32, count int, seed uint64) fixture.TrackSpec {
	tr := fixture.TrackSpec{ID: id, Handler: "soun", Timescale: 48000, Seed: seed}
	for i := 0; i < count; i++ {
		tr.Samples = append(tr.Samples, fixture.Sample{Delta: 1024, Size: uint32(48 + i%5), Sync: true})
	}
	return tr
}

func mustBuild(t *testing.T, spec fixture.MovieSpec) []byte {
	t.Helper()
	data, err := fixture.BuildMovie(spec)
	if err != nil {
		t.Fatalf("BuildMovie: %v", err)
	}
	return data
}

func TestParseRoundTripPreservesTimestamps(t *testing.T) {
	spec := fixture.MovieSpec{
		Brand:     "isom",
		Timescale: 1000,
		Tracks: []fixture.TrackSpec{
			videoTrack(1, 90000, 90, 11),
			audioTrack(2, 240, 22),
		},
	}
	data := mustBuild(t, spec)
	movie, err := Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if movie.MajorBrand != "isom" {
		t.Errorf("brand = %q", movie.MajorBrand)
	}
	if len(movie.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(movie.Tracks))
	}
	v := movie.Tracks[0]
	if v.ID != 1 || v.Kind != "video" || v.Timescale != 90000 {
		t.Errorf("video track header = %+v", v)
	}
	if len(v.Samples) != 90 {
		t.Fatalf("video samples = %d", len(v.Samples))
	}
	if len(v.Keyframes) != 3 {
		t.Errorf("video keyframes = %d, want 3", len(v.Keyframes))
	}
	// 1 tick at 90kHz = 11111.1ns rounded to 11111.
	if v.Samples[0].DTS != 0 || v.Samples[0].DTSNanos != 0 {
		t.Errorf("first sample DTS = %d/%d", v.Samples[0].DTS, v.Samples[0].DTSNanos)
	}
	wantDelta := media.Nano(33_333_333) // 3000 ticks @90k = 33.333ms
	if v.Samples[1].DeltaNanos != wantDelta {
		t.Errorf("delta nanos = %d, want %d", v.Samples[1].DeltaNanos, wantDelta)
	}
	for i, s := range v.Samples {
		if s.Offset+uint64(s.Size) > uint64(len(data)) {
			t.Fatalf("sample %d offset beyond file", i)
		}
		if s.DTS != int64(i*3000) {
			t.Fatalf("sample %d DTS = %d, want %d", i, s.DTS, i*3000)
		}
	}
	// Audio sample payloads must land inside the file and in order.
	a := movie.Tracks[1]
	for i := 1; i < len(a.Samples); i++ {
		if a.Samples[i].Offset <= a.Samples[i-1].Offset {
			t.Fatalf("audio offsets not ascending at %d", i)
		}
	}
}

func TestParseQuickTimeBrand(t *testing.T) {
	spec := fixture.MovieSpec{Brand: "qt  ", Timescale: 600, Tracks: []fixture.TrackSpec{videoTrack(1, 600, 10, 3)}}
	data := mustBuild(t, spec)
	movie, err := Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if movie.MajorBrand != "qt  " {
		t.Errorf("brand = %q", movie.MajorBrand)
	}
}

func TestParseVariableFrameRate(t *testing.T) {
	tr := fixture.TrackSpec{ID: 1, Handler: "vide", Timescale: 1000, Seed: 5}
	deltas := []uint32{40, 40, 33, 33, 33, 100}
	for i, d := range deltas {
		tr.Samples = append(tr.Samples, fixture.Sample{Delta: d, Size: 32, Sync: i == 0})
	}
	data := mustBuild(t, fixture.MovieSpec{Timescale: 1000, Tracks: []fixture.TrackSpec{tr}})
	movie, err := Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := movie.Tracks[0]
	if len(got.Samples) != len(deltas) {
		t.Fatalf("samples = %d", len(got.Samples))
	}
	var dts int64
	for i, d := range deltas {
		if got.Samples[i].DTS != dts {
			t.Errorf("sample %d DTS = %d, want %d", i, got.Samples[i].DTS, dts)
		}
		dts += int64(d)
	}
}

func TestParseCompositionOffsets(t *testing.T) {
	tr := fixture.TrackSpec{ID: 1, Handler: "vide", Timescale: 1000, Seed: 9}
	// B-frame style: decode order 0,2,1 with presentation offsets.
	offs := []int32{3000, 0, 1500}
	for i, o := range offs {
		tr.Samples = append(tr.Samples, fixture.Sample{Delta: 1000, Size: 16, Sync: i == 0, Ctts: o, HasCtt: true})
	}
	data := mustBuild(t, fixture.MovieSpec{Timescale: 1000, Tracks: []fixture.TrackSpec{tr}})
	movie, err := Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := movie.Tracks[0].Samples
	if s[0].PTS != 3000 || s[1].PTS != 1000 || s[2].PTS != 3500 {
		t.Fatalf("PTS = %d,%d,%d", s[0].PTS, s[1].PTS, s[2].PTS)
	}
	if s[0].DTSNanos != 0 || s[0].PTSNanos != media.Nano(3_000_000_000) {
		t.Fatalf("nanos = %d/%d", s[0].DTSNanos, s[0].PTSNanos)
	}
}

func TestParseRejectsTruncatedFile(t *testing.T) {
	data := mustBuild(t, fixture.MovieSpec{Timescale: 1000, Tracks: []fixture.TrackSpec{videoTrack(1, 30000, 20, 2)}})
	for _, cut := range []int{0, 4, 20, len(data) / 2, len(data) - 3} {
		if cut >= len(data) {
			continue
		}
		_, err := Parse(bytes.NewReader(data[:cut]), int64(cut))
		if err == nil {
			t.Fatalf("cut=%d parsed without error", cut)
		}
		cat := media.CategoryOf(err)
		if cat != media.TruncatedContainer && cat != media.CorruptSampleTable {
			t.Errorf("cut=%d category = %v (%v)", cut, cat, err)
		}
	}
}

func TestParseRejectsZeroTimescale(t *testing.T) {
	data := mustBuild(t, fixture.MovieSpec{Timescale: 1000, Tracks: []fixture.TrackSpec{videoTrack(1, 30000, 10, 4)}})
	patched := patchMdhdTimescale(t, data, 0)
	_, err := Parse(bytes.NewReader(patched), int64(len(patched)))
	if err == nil {
		t.Fatal("expected error for zero timescale")
	}
	if media.CategoryOf(err) != media.ZeroTimescale {
		t.Fatalf("category = %v (%v)", media.CategoryOf(err), err)
	}
	if e := media.AsError(err); !e.HasTrack || e.Track != 1 {
		t.Fatalf("error lacks track location: %v", err)
	}
}

func TestParseRejectsCorruptSampleTable(t *testing.T) {
	data := mustBuild(t, fixture.MovieSpec{Timescale: 1000, Tracks: []fixture.TrackSpec{videoTrack(1, 30000, 20, 6)}})
	// Rewrite the stsz sample count to a value larger than the table.
	patched := patchBoxUint32(t, data, "stsz", 12, 9999)
	_, err := Parse(bytes.NewReader(patched), int64(len(patched)))
	if err == nil {
		t.Fatal("expected error for corrupt stsz")
	}
	cat := media.CategoryOf(err)
	if cat != media.CorruptSampleTable && cat != media.TruncatedContainer {
		t.Fatalf("category = %v (%v)", cat, err)
	}
	if e := media.AsError(err); !e.HasTrack {
		t.Fatalf("error lacks track: %v", err)
	}
}

func TestParseRejectsOutOfOrderSampleOffsets(t *testing.T) {
	data := mustBuild(t, fixture.MovieSpec{Timescale: 1000, Tracks: []fixture.TrackSpec{videoTrack(1, 30000, 12, 8)}})
	// Point every chunk at the same offset, producing overlapping samples.
	patched := patchChunkOffsetsTo(t, data, 1)
	_, err := Parse(bytes.NewReader(patched), int64(len(patched)))
	if err == nil {
		t.Fatal("expected error for overlapping chunks")
	}
	cat := media.CategoryOf(err)
	if cat != media.CorruptSampleTable && cat != media.TruncatedContainer &&
		cat != media.OutOfOrderTimestamp {
		t.Fatalf("category = %v (%v)", cat, err)
	}
	if e := media.AsError(err); !e.HasTrack || !e.HasOff {
		t.Fatalf("overlap error lacks location: %v", err)
	}
}

func TestKeyframeTimesSortedAscending(t *testing.T) {
	tr := fixture.TrackSpec{ID: 1, Handler: "vide", Timescale: 1000, Seed: 1}
	for i := 0; i < 10; i++ {
		tr.Samples = append(tr.Samples, fixture.Sample{Delta: 100, Size: 8, Sync: i%3 == 0})
	}
	data := mustBuild(t, fixture.MovieSpec{Timescale: 1000, Tracks: []fixture.TrackSpec{tr}})
	movie, err := Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	times := movie.Tracks[0].KeyframeTimes()
	if len(times) != 4 {
		t.Fatalf("keyframe times = %d, want 4", len(times))
	}
	for i := 1; i < len(times); i++ {
		if times[i] <= times[i-1] {
			t.Fatalf("keyframe times not ascending: %v", times)
		}
	}
}

// patchChunkOffsetsTo rewrites every stco chunk offset to the first one,
// forcing sample payloads to overlap.
func patchChunkOffsetsTo(t *testing.T, data []byte, chunkSize int) []byte {
	t.Helper()
	idx := bytes.Index(data, []byte("stco"))
	if idx < 0 {
		t.Fatal("stco not found")
	}
	out := append([]byte(nil), data...)
	base := idx + 12 // after type(4)+version/flags(4)+entry_count(4)
	if base+chunkSize > len(out) {
		t.Fatal("stco truncated")
	}
	first := binary.BigEndian.Uint32(out[base:])
	for p := base + chunkSize; p+4 <= len(out); p += chunkSize {
		binary.BigEndian.PutUint32(out[p:], first)
	}
	return out
}

// patchMdhdTimescale rewrites the first mdhd timescale in the file.
func patchMdhdTimescale(t *testing.T, data []byte, ts uint32) []byte {
	t.Helper()
	return patchBoxUint32(t, data, "mdhd", 16, ts)
}

// patchBoxUint32 finds the first box of typ and overwrites the big-endian
// uint32 at payloadOffset (relative to the box header) with value.
func patchBoxUint32(t *testing.T, data []byte, typ string, payloadOffset int, value uint32) []byte {
	t.Helper()
	idx := bytes.Index(data, []byte(typ))
	for idx >= 0 {
		if idx >= 4 && int(idx)-4+8 <= len(data) {
			out := append([]byte(nil), data...)
			pos := idx + payloadOffset
			if pos+4 > len(out) {
				t.Fatalf("patch position %d beyond %d bytes", pos, len(out))
			}
			out[pos] = byte(value >> 24)
			out[pos+1] = byte(value >> 16)
			out[pos+2] = byte(value >> 8)
			out[pos+3] = byte(value)
			return out
		}
		next := bytes.Index(data[idx+1:], []byte(typ))
		if next < 0 {
			break
		}
		idx += 1 + next
	}
	t.Fatalf("box %q not found", typ)
	return nil
}

func TestSampleBytesBoundsChecked(t *testing.T) {
	spec := fixture.MovieSpec{Timescale: 1000, Tracks: []fixture.TrackSpec{videoTrack(1, 30000, 12, 4)}}
	data := mustBuild(t, spec)
	movie, err := Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := movie.SampleBytes(data, 0, 3)
	if err != nil {
		t.Fatalf("SampleBytes: %v", err)
	}
	if len(got) != int(movie.Tracks[0].Samples[3].Size) {
		t.Fatalf("len = %d, want %d", len(got), movie.Tracks[0].Samples[3].Size)
	}
	// Truncating the backing slice must be caught, not panicked on.
	if _, err := movie.SampleBytes(data[:100], 0, 3); err == nil {
		t.Fatal("out-of-range sample accepted")
	} else if media.CategoryOf(err) != media.TruncatedContainer {
		t.Fatalf("category = %v", media.CategoryOf(err))
	}
	if _, err := movie.SampleBytes(data, 5, 0); err == nil {
		t.Fatal("bad track index accepted")
	}
	if _, err := movie.SampleBytes(data, 0, 999); err == nil {
		t.Fatal("bad sample index accepted")
	}
}

func TestSourceTicksRoundTrip(t *testing.T) {
	spec := fixture.MovieSpec{Timescale: 1000, Tracks: []fixture.TrackSpec{videoTrack(1, 90000, 10, 2)}}
	data := mustBuild(t, spec)
	movie, err := Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	tr := movie.Tracks[0]
	for _, s := range tr.Samples {
		ticks, err := tr.SourceTicks(s.DTSNanos)
		if err != nil {
			t.Fatalf("SourceTicks: %v", err)
		}
		if ticks != s.DTS {
			t.Fatalf("ticks = %d, want %d", ticks, s.DTS)
		}
	}
}
