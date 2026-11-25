package continuity

import (
	"strings"
	"testing"
)

func sourceInit(t *testing.T, label string, tracks ...TrackSpec) TrackSource {
	t.Helper()
	init, err := ParseInit(label+"/init.mp4", BuildInit(InitSpec{Tracks: tracks}))
	if err != nil {
		t.Fatalf("%s init: %v", label, err)
	}
	return TrackSource{Label: label, Init: init}
}

func TestBuildTrackMapPrimaryBackup(t *testing.T) {
	v := videoTrack()
	a := audioTrack()
	primary := sourceInit(t, "main", v, a)
	backup := sourceInit(t, "backup", v, a)
	tm, err := BuildTrackMap([]TrackSource{primary, backup})
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.Entries) != 4 {
		t.Fatalf("entries=%d", len(tm.Entries))
	}
	if len(tm.Targets) != 2 {
		t.Fatalf("targets=%d", len(tm.Targets))
	}
	m, ok := tm.Lookup("backup", 1)
	if !ok || m.Target != "video-avc1" {
		t.Fatalf("lookup=%+v,%v", m, ok)
	}
	if m.StableKey != "backup/video-avc1/1" {
		t.Fatalf("stable key=%q", m.StableKey)
	}
	if len(tm.MissingTargets()) != 0 {
		t.Fatalf("missing=%v", tm.MissingTargets())
	}
	if len(tm.Describe()) != 4 {
		t.Fatalf("describe=%v", tm.Describe())
	}
}

func TestBuildTrackMapDeterministicHash(t *testing.T) {
	v, a := videoTrack(), audioTrack()
	build := func() string {
		tm, err := BuildTrackMap([]TrackSource{
			sourceInit(t, "main", v, a),
			sourceInit(t, "backup", v, a),
		})
		if err != nil {
			t.Fatal(err)
		}
		return tm.Hash()
	}
	if build() != build() {
		t.Fatal("mapping hash must be stable across builds")
	}
}

func TestBuildTrackMapRejectsCodecConfigMismatch(t *testing.T) {
	v1 := videoTrack()
	v2 := videoTrack()
	v2.CodecConfig = append([]byte(nil), v1.CodecConfig...)
	v2.CodecConfig[3] = 0x28 // different level
	_, err := BuildTrackMap([]TrackSource{
		sourceInit(t, "main", v1),
		sourceInit(t, "backup", v2),
	})
	if err == nil || !IsKind(err, KindMapping) {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "not lossless") {
		t.Fatalf("error should explain losslessness: %v", err)
	}
}

func TestBuildTrackMapRejectsTimescaleMismatch(t *testing.T) {
	v1 := videoTrack()
	v2 := videoTrack()
	v2.Timescale = 45000
	_, err := BuildTrackMap([]TrackSource{
		sourceInit(t, "main", v1),
		sourceInit(t, "backup", v2),
	})
	if err == nil || !IsKind(err, KindMapping) {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildTrackMapRejectsEmpty(t *testing.T) {
	if _, err := BuildTrackMap(nil); err == nil || !IsKind(err, KindMapping) {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildTrackMapDuplicateLabel(t *testing.T) {
	v := videoTrack()
	_, err := BuildTrackMap([]TrackSource{
		sourceInit(t, "main", v),
		sourceInit(t, "main", v),
	})
	if err == nil || !IsKind(err, KindMapping) {
		t.Fatalf("err=%v", err)
	}
}

func TestTrackMapTargetLookup(t *testing.T) {
	tm, err := BuildTrackMap([]TrackSource{sourceInit(t, "main", videoTrack())})
	if err != nil {
		t.Fatal(err)
	}
	tgt, ok := tm.Target("video-avc1")
	if !ok || tgt.Codec != CodecAVC || tgt.Timescale != 90000 {
		t.Fatalf("target=%+v", tgt)
	}
	if _, ok := tm.Target("audio-mp4a"); ok {
		t.Fatal("unexpected audio target")
	}
}
