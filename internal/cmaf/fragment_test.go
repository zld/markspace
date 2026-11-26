package cmaf

import (
	"testing"

	"rtx/internal/model"
)

func sampleFragment(t *testing.T, spec FragmentSpec) *Fragment {
	t.Helper()
	init, err := BuildInit(DefaultInitSpec())
	if err != nil {
		t.Fatal(err)
	}
	frag, err := BuildFragment(spec)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseBytes("frag.m4s", frag)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseFragment(f)
	if err != nil {
		t.Fatal(err)
	}
	// The init segment must also parse.
	initFile, err := ParseBytes("init.mp4", init)
	if err != nil {
		t.Fatal(err)
	}
	initFrag, err := ParseFragment(initFile)
	if err != nil {
		t.Fatal(err)
	}
	if !initFrag.IsInit {
		t.Fatal("init segment was not recognised as init")
	}
	return parsed
}

func TestFragmentRoundTrip(t *testing.T) {
	spec := FragmentSpec{
		Sequence: 3,
		VideoTFD: 90000,
		AudioTFD: 48000,
		Video: []MuxSample{
			{Data: []byte("v1"), Duration: 3600, Key: true},
			{Data: []byte("v2"), Duration: 3600},
		},
		Audio: []MuxSample{{Data: make([]byte, 1024), Duration: 1024}},
	}
	frag := sampleFragment(t, spec)
	if frag.MFHDSequence == nil || *frag.MFHDSequence != 3 {
		t.Fatalf("mfhd = %v", frag.MFHDSequence)
	}
	if len(frag.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(frag.Tracks))
	}
	if frag.Tracks[0].TrackID != 1 || frag.Tracks[0].BaseMediaDecodeTime != 90000 {
		t.Fatalf("video track = %+v", frag.Tracks[0])
	}
	if frag.Tracks[1].TrackID != 2 || frag.Tracks[1].BaseMediaDecodeTime != 48000 {
		t.Fatalf("audio track = %+v", frag.Tracks[1])
	}
	if !frag.Tracks[0].Samples[0].IsSync() {
		t.Fatal("first video sample should be a keyframe")
	}
	if frag.Tracks[0].Samples[1].IsSync() {
		t.Fatal("second video sample should not be a keyframe")
	}
}

func TestFragmentMissingMFHDIsNil(t *testing.T) {
	frag := sampleFragment(t, FragmentSpec{
		Sequence: 1,
		OmitMFHD: true,
		Video:    []MuxSample{{Data: []byte("a"), Duration: 3600, Key: true}},
	})
	if frag.MFHDSequence != nil {
		t.Fatalf("mfhd = %v, want nil", frag.MFHDSequence)
	}
}

func TestTruncatedBoxReportsFirstUnreadableOffset(t *testing.T) {
	full, err := BuildFragment(FragmentSpec{
		Sequence: 1,
		Video:    []MuxSample{{Data: make([]byte, 100), Duration: 3600, Key: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Cut in the middle of the mdat payload; the mdat header claims more than
	// remains, so the first unreadable byte is the mdat box offset.
	cut := len(full) - 20
	_, err = ParseBytes("trunc.m4s", full[:cut])
	if err == nil {
		t.Fatal("expected truncation error")
	}
	e := errAs(t, err)
	if e.Code != model.CodeTruncatedBox {
		t.Fatalf("code = %s, want %s", e.Code, model.CodeTruncatedBox)
	}
	if !e.HasByte || e.Offset >= int64(cut) {
		t.Fatalf("offset = %d (has=%v), want < %d", e.Offset, e.HasByte, cut)
	}
}

func TestHeaderLargerThanFile(t *testing.T) {
	// A top-level box claims 4096 bytes but the file is 16.
	data := make([]byte, 16)
	putU32(data[0:], 4096)
	copy(data[4:8], "moof")
	data[8] = 'x'
	_, err := ParseBytes("bad.m4s", data)
	if err == nil {
		t.Fatal("expected error")
	}
	e := errAs(t, err)
	if e.Code != model.CodeTruncatedBox || e.Offset != 0 {
		t.Fatalf("err = %+v", e)
	}
}

func TestBoxSizeSmallerThanHeader(t *testing.T) {
	data := make([]byte, 16)
	putU32(data[0:], 4)
	copy(data[4:8], "moof")
	_, err := ParseBytes("bad.m4s", data)
	if err == nil {
		t.Fatal("expected error")
	}
	if e := errAs(t, err); e.Code != CodeCMAFInvalid {
		t.Fatalf("code = %s", e.Code)
	}
}

func TestManifestRejectsEscapingPath(t *testing.T) {
	base := `{"version":1,"media_rate":{"num":720000,"den":1},"sources":[{"id":"a","init":"init.mp4","fragments":[{"path":"../escape.m4s","order":1}],"rtp_trace":"t.jsonl","rtp_anchors":[]}],"events":[]}`
	_, err := ParseManifest("m.json", []byte(base))
	if err == nil {
		t.Fatal("expected escaping path to be rejected")
	}
}

func TestManifestRejectsAbsolutePath(t *testing.T) {
	base := `{"version":1,"media_rate":{"num":720000,"den":1},"sources":[{"id":"a","init":"/etc/passwd","fragments":[],"rtp_trace":"t.jsonl","rtp_anchors":[]}],"events":[]}`
	_, err := ParseManifest("m.json", []byte(base))
	if err == nil {
		t.Fatal("expected absolute path to be rejected")
	}
}

func TestManifestDuplicateFragmentOrder(t *testing.T) {
	m := &Manifest{Version: 1}
	m.MediaRate.Num, m.MediaRate.Den = 720000, 1
	m.Sources = []Source{{ID: "a", Fragments: []FragmentRef{
		{Path: "a.m4s", Order: 1}, {Path: "b.m4s", Order: 1},
	}}}
	if _, err := m.Sources[0].FragmentsInOrder(); err == nil {
		t.Fatal("expected duplicate order error")
	}
}

func TestManifestFragmentsSorted(t *testing.T) {
	s := Source{Fragments: []FragmentRef{{Path: "b.m4s", Order: 2}, {Path: "a.m4s", Order: 1}}}
	out, err := s.FragmentsInOrder()
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Path != "a.m4s" || out[1].Path != "b.m4s" {
		t.Fatalf("order = %+v", out)
	}
}

func errAs(t *testing.T, err error) *model.Err {
	t.Helper()
	e, ok := model.As(err)
	if !ok {
		t.Fatalf("error %v is not a *model.Err", err)
	}
	return e
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

func TestPatchTFDTKeepsOtherBytes(t *testing.T) {
	raw, err := BuildFragment(FragmentSpec{
		Sequence: 1, VideoTFD: 12345,
		Video: []MuxSample{{Data: []byte("abcdef"), Duration: 3600, Key: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseBytes("f.m4s", raw)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseFragment(parsed)
	if err != nil {
		t.Fatal(err)
	}
	track := f.Tracks[0]
	patched, err := PatchTFDT(raw, &track, 99999)
	if err != nil {
		t.Fatal(err)
	}
	if len(patched) != len(raw) {
		t.Fatalf("length changed: %d != %d", len(patched), len(raw))
	}
	p2, err := ParseBytes("f.m4s", patched)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := ParseFragment(p2)
	if err != nil {
		t.Fatal(err)
	}
	if f2.Tracks[0].BaseMediaDecodeTime != 99999 {
		t.Fatalf("tfdt = %d, want 99999", f2.Tracks[0].BaseMediaDecodeTime)
	}
	s := f2.Tracks[0].Samples[0]
	if got := string(patched[s.DataOffset : s.DataOffset+int64(s.Size)]); got != "abcdef" {
		t.Fatalf("sample data changed: %q", got)
	}
}

func TestPatchTFDTRejectsOverflowForV0(t *testing.T) {
	// A v0 tfdt field is 32 bits; a larger value must be rejected, not truncated.
	track := &TrackFragment{TFDTVersion: 0, TFDTOffset: 0}
	buf := make([]byte, 4)
	if _, err := PatchTFDT(buf, track, 1<<33); err == nil {
		t.Fatal("expected overflow rejection")
	}
}
