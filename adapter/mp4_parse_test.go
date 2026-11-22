package adapter

import (
	"bytes"
	"strings"
	"testing"

	"mediacore/internal/mp4box"
	"mediacore/media"
)

func validTracks() []mp4box.TrackSpec {
	return []mp4box.TrackSpec{
		{
			TrackID: 1, Timescale: 44100,
			SampleDur:  repeatU32(1024, 8),
			SampleSize: repeatU32(256, 8),
			StartPTS:   4410, // 100 ms non-zero presentation origin
		},
		{
			TrackID: 2, Timescale: 90000, Video: true,
			SampleDur:  []uint32{3000, 3000, 3000, 6000, 3000}, // variable FPS
			SampleSize: []uint32{120, 80, 120, 90, 110},
			Keyframes:  []uint32{1, 4}, // sparse keyframes
			EmptyHead:  500,
		},
	}
}

func repeatU32(v uint32, n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestMP4ParseHappyPath(t *testing.T) {
	built := mp4box.Build(validTracks())
	a := NewMP4AdapterV1()
	m, err := a.ParseMP4(bytes.NewReader(built.Data), int64(len(built.Data)))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(m.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(m.Tracks))
	}
	audio := m.Tracks[0]
	if audio.Kind != KindAudio {
		t.Fatalf("audio kind wrong: %+v", audio.Kind)
	}
	// The edit list starts presenting at media time 100 ms. Samples at 0,
	// 23.2, 46.4 and 69.7 ms fall in the pre-start region and are clipped;
	// the three samples at >= 92.9 ms are presented from movie time zero.
	if len(audio.Samples) != 3 {
		t.Fatalf("presented audio samples = %d, want 3", len(audio.Samples))
	}
	if audio.StartPTS <= 0 {
		t.Fatal("non-zero start PTS not preserved")
	}
	if audio.Samples[0].PTS < 0 {
		t.Fatalf("first presented sample PTS must be non-negative, got %d", audio.Samples[0].PTS)
	}
	// presentation begins exactly one sample-period before the 100 ms origin
	if audio.Samples[0].PTS != 16099773 {
		t.Fatalf("unexpected first presented PTS %d", audio.Samples[0].PTS)
	}
	// every audio PTS must be strictly monotonic after edit mapping
	for i := 1; i < len(audio.Samples); i++ {
		if audio.Samples[i].PTS <= audio.Samples[i-1].PTS {
			t.Fatalf("audio PTS not monotonic at %d", i)
		}
	}
	video := m.Tracks[1]
	if video.Kind != KindVideo {
		t.Fatal("handler type not detected as video")
	}
	var keys int
	for _, s := range video.Samples {
		if s.IsKeyframe {
			keys++
		}
	}
	if keys != 2 {
		t.Fatalf("sparse keyframes = %d, want 2", keys)
	}
	if len(video.Edits) != 2 {
		t.Fatalf("empty head edit not mapped: %d edits", len(video.Edits))
	}
	if video.Edits[0].MediaTime != -1 {
		t.Fatal("first edit should be an empty gap")
	}
	// offsets land inside the mdat payload
	for _, tr := range m.Tracks {
		for _, s := range tr.Samples {
			if s.ByteOffset < built.MdatOffset || s.ByteOffset >= int64(len(built.Data)) {
				t.Fatalf("sample offset %d outside file", s.ByteOffset)
			}
		}
	}
}

func TestMP4ParseDeterministic(t *testing.T) {
	b1 := mp4box.Build(validTracks())
	b2 := mp4box.Build(validTracks())
	if !bytes.Equal(b1.Data, b2.Data) {
		t.Fatal("fixture builder is not deterministic")
	}
	a := NewMP4AdapterV1()
	m1, err := a.ParseMP4(bytes.NewReader(b1.Data), int64(len(b1.Data)))
	if err != nil {
		t.Fatal(err)
	}
	m2, err := a.ParseMP4(bytes.NewReader(b2.Data), int64(len(b2.Data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(m1.Tracks) != len(m2.Tracks) {
		t.Fatal("track count differs across runs")
	}
	for i := range m1.Tracks {
		s1, s2 := m1.Tracks[i].Samples, m2.Tracks[i].Samples
		if len(s1) != len(s2) {
			t.Fatal("sample count differs across runs")
		}
		for j := range s1 {
			if s1[j] != s2[j] {
				t.Fatalf("sample %d/%d differs across runs", i, j)
			}
		}
	}
}

func TestMP4CorruptInputsAreLocated(t *testing.T) {
	built := mp4box.Build(validTracks())
	a := NewMP4AdapterV1()

	// 1. truncated header
	if _, err := a.ParseMP4(bytes.NewReader(built.Data[:4]), 4); err == nil {
		t.Fatal("truncated file accepted")
	}

	// 2. random bytes: must produce a rejectable media.Error, never panic
	garbage := bytes.Repeat([]byte{0x7a}, 512)
	_, err := a.ParseMP4(bytes.NewReader(garbage), int64(len(garbage)))
	if err == nil {
		t.Fatal("garbage accepted as MP4")
	}
	me, ok := media.AsError(err)
	if !ok || !me.Rejectable() {
		t.Fatalf("garbage error not rejectable: %v", err)
	}

	// 3. offset tampering: locate the first stco box and corrupt its final
	//    chunk offset so it points beyond EOF
	tampered := append([]byte(nil), built.Data...)
	idx := bytes.Index(tampered, []byte("stco"))
	if idx < 0 {
		t.Fatal("stco not found")
	}
	countPos := idx + 8 // full-box header (4) + "stco" (4)
	count := int(binU32(tampered[countPos:]))
	if count != 8 {
		t.Fatalf("unexpected stco count %d", count)
	}
	lastOffPos := countPos + 4 + (count-1)*4
	putU32(tampered[lastOffPos:], uint32(len(tampered))+4096)
	_, err3 := a.ParseMP4(bytes.NewReader(tampered), int64(len(tampered)))
	me2, ok := media.AsError(err3)
	if !ok || me2.Code != media.CodeOffsetOutOfRange {
		t.Fatalf("want offset_out_of_range, got %v", err)
	}
	if me2.ByteOffset < 0 {
		t.Fatal("offset error must carry a byte location")
	}
}

func binU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func putU32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func TestMP4MissingTableRejected(t *testing.T) {
	// hand-built moov with a trak lacking stbl tables
	trak := mp4box.NewContainer("trak",
		mp4box.NewContainer("mdia",
			mp4box.NewLeaf("mdhd", mp4box.FullHeader(0, 0,
				mp4box.Concat(mp4box.U32(0), mp4box.U32(0), mp4box.U32(48000), mp4box.U32(100), make([]byte, 4)))),
			mp4box.NewContainer("minf", mp4box.NewContainer("stbl")),
		))
	moov := mp4box.NewContainer("moov", trak)
	data := append(mp4box.NewLeaf("ftyp", []byte("isomiso2")).Bytes(), moov.Bytes()...)
	_, err := NewMP4AdapterV1().ParseMP4(bytes.NewReader(data), int64(len(data)))
	me, ok := media.AsError(err)
	if !ok || me.Code != media.CodeMissingTable {
		t.Fatalf("want missing_sample_table, got %v", err)
	}
}

func TestMP4BadEditSegmentRejected(t *testing.T) {
	specs := validTracks()
	specs[0].StartPTS = 1 << 30 // far beyond track duration
	built := mp4box.Build(specs)
	_, err := NewMP4AdapterV1().ParseMP4(bytes.NewReader(built.Data), int64(len(built.Data)))
	me, ok := media.AsError(err)
	if !ok || me.Code != media.CodeBadEditSegment {
		t.Fatalf("want bad_edit_segment, got %v", err)
	}
	if !strings.Contains(me.Error(), "elst") {
		t.Fatal("error detail should mention elst")
	}
}
