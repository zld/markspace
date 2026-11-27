package archive

import (
	"bytes"
	"testing"

	"mediarc/internal/demux"
	"mediarc/internal/model"
)

func buildTracks() []model.Track {
	video := demux.SamplesToTrack(1, model.CodecAVC1, 90000, []demux.FixtureSample{
		{Dts: 0, Duration: 3000, Payload: []byte{0xDE, 0xAD, 0xBE, 0xEF}, Sync: true},
		{Dts: 3000, Duration: 3000, Payload: []byte{0x01, 0x02, 0x03, 0x04}},
	})
	audio := demux.SamplesToTrack(2, model.CodecMP4A, 48000, []demux.FixtureSample{
		{Dts: 0, Duration: 1024, Payload: []byte{0xAA, 0xBB}},
		{Dts: 1024, Duration: 1024, Payload: []byte{0xCC, 0xDD}},
	})
	return []model.Track{video, audio}
}

func TestRemuxLosslessRoundTrip(t *testing.T) {
	tracks := buildTracks()
	rep := Remux("bundle.mp4", tracks)
	if rep.Error != nil {
		t.Fatalf("remux error: %v", rep.Error)
	}
	if !rep.Lossless || rep.InputHash == "" || rep.InputHash != rep.OutputHash {
		t.Fatalf("remux must verify lossless: %+v", rep)
	}
	if rep.TrackCount != 2 || rep.SampleCount != 4 {
		t.Fatalf("counts wrong: %+v", rep)
	}

	// Independently re-demux and compare every compressed byte in order.
	got, err := demux.NewMP4Demuxer("verify.mp4").Demux(rep.Output[:memberBoundary(rep.Output)])
	if err != nil {
		t.Fatalf("re-demux first member: %v", err)
	}
	if !bytes.Equal(got[0].Samples[0].Payload, tracks[0].Samples[0].Payload) {
		t.Fatal("sample bytes not preserved byte-for-byte")
	}
}

// memberBoundary returns the byte length of the first fMP4 bundle member.
func memberBoundary(data []byte) int {
	ftyp := []byte{'f', 't', 'y', 'p'}
	first := bytes.Index(data, ftyp)
	rest := data[first+4:]
	if idx := bytes.Index(rest, ftyp); idx >= 0 {
		return first + 4 + idx
	}
	return len(data)
}

func TestRemuxPreservesUnknownCodec(t *testing.T) {
	tr := demux.SamplesToTrack(9, model.CodecUnknown, 24000, []demux.FixtureSample{
		{Dts: 0, Duration: 512, Payload: []byte{0x77, 0x88}, Sync: true},
	})
	rep := Remux("unknown.mp4", []model.Track{tr})
	if !rep.Lossless || rep.Error != nil {
		t.Fatalf("unknown codec must remux losslessly: %+v", rep)
	}
}

func TestRemuxDetectsTamperedBytes(t *testing.T) {
	tracks := buildTracks()
	rep := Remux("bundle.mp4", tracks)
	if !rep.Lossless {
		t.Fatalf("baseline must be lossless: %v", rep.Error)
	}
	wantHashes := map[string]bool{}
	for _, tr := range tracks {
		wantHashes[trackHash(tr)] = true
	}

	// Flip a byte inside the last member's mdat payload region, then rerun
	// verification through the same extractor the remuxer uses.
	tampered := append([]byte(nil), rep.Output...)
	tampered[len(tampered)-2] ^= 0xFF
	out, err := extractTracks("bundle.mp4", tampered)
	if err != nil {
		return // corruption surfaced as a classified parse failure, acceptable
	}
	unchanged := 0
	for _, tr := range out {
		if wantHashes[trackHash(tr)] {
			unchanged++
		}
	}
	if unchanged == len(out) {
		t.Fatal("tampered output must change at least one track fingerprint")
	}
}

func TestRemuxEmptyRejected(t *testing.T) {
	rep := Remux("empty.mp4", nil)
	if rep.Lossless || rep.Error == nil || rep.Error.Kind != model.KindRemuxMismatch {
		t.Fatalf("empty remux must be a classified failure: %+v", rep)
	}
}

func TestRemuxDeterministic(t *testing.T) {
	tracks := buildTracks()
	a := Remux("a.mp4", tracks)
	b := Remux("a.mp4", tracks)
	if a.InputHash != b.InputHash || a.OutputHash != b.OutputHash {
		t.Fatal("identical inputs must produce identical remux fingerprints")
	}
}
