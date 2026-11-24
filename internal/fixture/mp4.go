package fixture

import "fmt"

// TrackSpec describes one synthesized track.
type TrackSpec struct {
	TrackID     uint32
	Handler     string // "vide" or "soun"
	Timescale   uint32
	SampleCount uint32
	// Delta is the constant per-sample delta in track ticks. VFR content
	// supplies SttsRuns instead.
	Delta uint32
	// SttsRuns, when non-empty, overrides Delta for VFR content.
	SttsRuns []SttsRun
	// SyncEvery marks every nth sample a sync point; 0 means no stss box.
	SyncEvery uint32
	// MissingIndex removes the stts box (undecidable index).
	MissingIndex bool
	// TruncatedIndex cuts the stts payload after its entry count.
	TruncatedIndex bool
	// Edits optionally adds an elst box (version 0 entries).
	Edits []ElstSpec
	// Width/Height are fixed-point 16.16 values.
	Width, Height uint32
}

// SttsRun is one (count, delta) timing run.
type SttsRun struct {
	Count, Delta uint32
}

// ElstSpec describes one version-0 edit entry.
type ElstSpec struct {
	SegmentDuration uint32
	MediaTime       int32
}

// MediaSpec is a complete synthesized asset.
type MediaSpec struct {
	MovieScale uint32
	Tracks     []TrackSpec
	// Trefs installs reference edges between track IDs.
	Trefs []TrefSpec
}

// TrefSpec adds one tref child box of RefType containing ToTrackIDs.
type TrefSpec struct {
	FromTrack  uint32
	RefType    string
	ToTrackIDs []uint32
}

// BuildMP4 serializes a valid (or deliberately damaged) MP4 byte stream.
func BuildMP4(spec MediaSpec) []byte {
	out := Box("ftyp", concat(
		[]byte("isom"), u32(0x200),
		[]byte("isom"), []byte("iso2"), []byte("mp41"),
	))
	traks := make([][]byte, 0, len(spec.Tracks))
	for _, ts := range spec.Tracks {
		traks = append(traks, buildTrak(spec, ts))
	}
	moovChildren := concat(append([][]byte{buildMvhd(spec)}, traks...)...)
	out = append(out, Box("moov", moovChildren)...)
	// A zero-length mdat keeps sample offset interpretation unambiguous.
	out = append(out, Box("mdat", []byte{})...)
	return out
}

func buildMvhd(spec MediaSpec) []byte {
	scale := spec.MovieScale
	if scale == 0 {
		scale = 1000
	}
	payload := make([]byte, 0, 100)
	payload = append(payload, u32(0)...)             // creation
	payload = append(payload, u32(0)...)             // modification
	payload = append(payload, u32(scale)...)         // timescale
	payload = append(payload, u32(10*scale)...)      // 10 s duration
	payload = append(payload, u32(0x00010000)...)    // rate 1.0
	payload = append(payload, []byte{0x01, 0x00}...) // volume
	payload = append(payload, make([]byte, 10)...)   // reserved
	matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	for _, m := range matrix {
		payload = append(payload, u32(m)...)
	}
	payload = append(payload, make([]byte, 24)...) // pre_defined
	payload = append(payload, u32(uint32(len(spec.Tracks)+1))...)
	return FullBox("mvhd", payload)
}

func buildTrak(spec MediaSpec, ts TrackSpec) []byte {
	tkhd := buildTkhd(ts)
	mdia := Box("mdia", concat(buildMdhd(ts), buildHdlr(ts), buildMinf(ts)))
	parts := [][]byte{tkhd, mdia}
	if len(ts.Edits) > 0 {
		parts = append(parts, buildElst(ts))
	}
	if refs := trefsFor(spec, ts.TrackID); len(refs) > 0 {
		parts = append(parts, refs...)
	}
	return Box("trak", concat(parts...))
}

func buildTkhd(ts TrackSpec) []byte {
	payload := make([]byte, 0, 84)
	payload = append(payload, u32(0)...) // creation/modification
	payload = append(payload, u32(0)...)
	payload = append(payload, u32(ts.TrackID)...)
	payload = append(payload, u32(0)...) // reserved
	payload = append(payload, u32(ts.SampleCount*ts.Delta)...)
	payload = append(payload, make([]byte, 8)...) // reserved
	payload = append(payload, make([]byte, 4)...) // layer/alternate
	vol := []byte{0, 0}
	if ts.Handler == "soun" {
		vol = []byte{0x01, 0x00}
	}
	payload = append(payload, vol...)
	payload = append(payload, make([]byte, 2)...)
	matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	for _, m := range matrix {
		payload = append(payload, u32(m)...)
	}
	payload = append(payload, u32(ts.Width)...)
	payload = append(payload, u32(ts.Height)...)
	return FullBox("tkhd", payload)
}

func buildMdhd(ts TrackSpec) []byte {
	scale := ts.Timescale
	if scale == 0 {
		scale = 1000
	}
	dur := uint32(0)
	if ts.Delta > 0 {
		dur = ts.SampleCount * ts.Delta
	}
	payload := concat(u32(0), u32(0), u32(scale), u32(dur),
		[]byte{0x55, 0xc4, 0x00, 0x00}) // undetermined language + pre-defined
	return FullBox("mdhd", payload)
}

func buildHdlr(ts TrackSpec) []byte {
	name := "VideoHandler\x00"
	if ts.Handler == "soun" {
		name = "SoundHandler\x00"
	}
	payload := concat(u32(0), []byte(ts.Handler), make([]byte, 12), []byte(name))
	return FullBox("hdlr", payload)
}

func buildMinf(ts TrackSpec) []byte {
	return Box("minf", concat(buildStbl(ts)))
}

func buildStbl(ts TrackSpec) []byte {
	children := make([][]byte, 0, 6)
	if !ts.MissingIndex {
		children = append(children, buildStts(ts))
	}
	if ts.SyncEvery > 0 {
		children = append(children, buildStss(ts))
	}
	children = append(children, buildStsc(), buildStsz(ts), buildStco())
	return Box("stbl", concat(children...))
}

func buildStts(ts TrackSpec) []byte {
	runs := ts.SttsRuns
	if len(runs) == 0 && ts.Delta > 0 {
		runs = []SttsRun{{Count: ts.SampleCount, Delta: ts.Delta}}
	}
	payload := u32(uint32(len(runs)))
	for _, r := range runs {
		payload = append(payload, u32(r.Count)...)
		payload = append(payload, u32(r.Delta)...)
	}
	if ts.TruncatedIndex {
		// Valid box header, but the run payload is cut after the count.
		return FullBox("stts", payload[:4])
	}
	return FullBox("stts", payload)
}

func buildStss(ts TrackSpec) []byte {
	var ids []byte
	for n := uint32(1); n <= ts.SampleCount; n++ {
		if n == 1 || n%ts.SyncEvery == 0 {
			ids = append(ids, u32(n)...)
		}
	}
	return FullBox("stss", concat(u32(uint32(len(ids)/4)), ids))
}

func buildStsc() []byte {
	return FullBox("stsc", concat(u32(1), u32(1), u32(1), u32(1)))
}

func buildStsz(ts TrackSpec) []byte {
	// Constant sample size (100 bytes): the EntrySize array stays empty and
	// SampleCount remains authoritative for the number of samples.
	return FullBox("stsz", concat(u32(100), u32(ts.SampleCount)))
}

func buildStco() []byte {
	// One chunk at offset zero; payload offsets are irrelevant to timing.
	return FullBox("stco", concat(u32(1), u32(0)))
}

func buildElst(ts TrackSpec) []byte {
	payload := u32(uint32(len(ts.Edits)))
	for _, e := range ts.Edits {
		payload = append(payload, u32(e.SegmentDuration)...)
		payload = append(payload, i32(e.MediaTime)...)
		payload = append(payload, u32(0x00010000)...) // rate 1.0
	}
	return Box("edts", FullBox("elst", payload))
}

func trefsFor(spec MediaSpec, from uint32) [][]byte {
	var out [][]byte
	for _, tr := range spec.Trefs {
		if tr.FromTrack != from {
			continue
		}
		var ids []byte
		for _, id := range tr.ToTrackIDs {
			ids = append(ids, u32(id)...)
		}
		out = append(out, Box("tref", FullBox(tr.RefType, ids)))
	}
	return out
}

// String renders a short spec label for test messages.
func (s MediaSpec) String() string {
	return fmt.Sprintf("tracks=%d", len(s.Tracks))
}
