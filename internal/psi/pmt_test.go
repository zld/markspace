package psi

import (
	"errors"
	"testing"
)

func TestPMTCommitsWithStreamClassification(t *testing.T) {
	tracker := NewPMTTracker()
	streams := []ElementaryStream{
		{PID: 256, StreamType: 0x1b, Kind: StreamVideo},
		{PID: 257, StreamType: 0x0f, Kind: StreamAudio},
		{PID: 258, StreamType: 0x06, Kind: StreamPrivate},
		{PID: 259, StreamType: 0x81, Kind: StreamAudio}, // AC-3 sits in user-private range
		{PID: 260, StreamType: 0x09, Kind: StreamOther},
	}
	sec := BuildPMTSection(1, 256, 0, streams)
	pmt, changed, err := tracker.Push(patSection(100, 0, sec))
	if err != nil || pmt == nil || changed {
		t.Fatalf("first commit: pmt=%v changed=%v err=%v", pmt, changed, err)
	}
	if pmt.ProgramNumber != 1 || pmt.Version != 0 || pmt.PCRPID != 256 {
		t.Fatalf("PMT header fields wrong: %+v", pmt)
	}
	if len(pmt.Streams) != 5 {
		t.Fatalf("expected 5 streams, got %d", len(pmt.Streams))
	}
	for i, want := range streams {
		if pmt.Streams[i].PID != want.PID || pmt.Streams[i].Kind != want.Kind {
			t.Fatalf("stream %d wrong: got %+v want kind %s", i, pmt.Streams[i], want.Kind)
		}
	}
}

func TestPMTVersionChangePerPID(t *testing.T) {
	tracker := NewPMTTracker()
	v0 := BuildPMTSection(1, 256, 0, []ElementaryStream{{256, 0x1b, StreamVideo}})
	v1 := BuildPMTSection(1, 300, 1, []ElementaryStream{{300, 0x1b, StreamVideo}})

	if _, changed, _ := tracker.Push(patSection(100, 0, v0)); changed {
		t.Fatal("initial PMT version is not a change")
	}
	if _, changed, _ := tracker.Push(patSection(100, 0, v0)); changed {
		t.Fatal("duplicate PMT version must not count as change")
	}
	pmt, changed, err := tracker.Push(patSection(100, 0, v1))
	if err != nil || !changed || pmt.PCRPID != 300 || len(pmt.Streams) != 1 {
		t.Fatalf("PMT v1 wrong: %+v changed=%v err=%v", pmt, changed, err)
	}
}

func TestPMTBadCRCKeepsPrevious(t *testing.T) {
	tracker := NewPMTTracker()
	v0 := BuildPMTSection(1, 256, 0, []ElementaryStream{{256, 0x1b, StreamVideo}})
	if _, _, err := tracker.Push(patSection(100, 0, v0)); err != nil {
		t.Fatal(err)
	}
	bad := CorruptCRC(BuildPMTSection(1, 256, 1, []ElementaryStream{{777, 0x1b, StreamVideo}}))
	if _, _, err := tracker.Push(patSection(100, 0, bad)); !errors.Is(err, ErrBadCRC) {
		t.Fatalf("expected ErrBadCRC, got %v", err)
	}
	cur := tracker.Current(100)
	if cur == nil || cur.Version != 0 || cur.Streams[0].PID != 256 {
		t.Fatalf("bad CRC overwrote committed PMT: %+v", cur)
	}
}

func TestPMTTracksTwoPMTPIDsIndependently(t *testing.T) {
	tracker := NewPMTTracker()
	p1 := BuildPMTSection(1, 256, 3, []ElementaryStream{{256, 0x1b, StreamVideo}})
	p2 := BuildPMTSection(2, 356, 7, []ElementaryStream{{356, 0x0f, StreamAudio}})
	if _, _, err := tracker.Push(patSection(100, 0, p1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tracker.Push(patSection(101, 0, p2)); err != nil {
		t.Fatal(err)
	}
	if tracker.Current(100).ProgramNumber != 1 || tracker.Current(101).ProgramNumber != 2 {
		t.Fatalf("per-PID PMT state mixed: %+v %+v", tracker.Current(100), tracker.Current(101))
	}
}

// handBuildPMTSection creates a PMT section with explicit section numbers so
// multi-section PMT behavior can be exercised.
func handBuildPMTSection(program, pcrPID uint16, version, num, last uint8, streams []ElementaryStream) []byte {
	body := []byte{0xe0 | byte((pcrPID>>8)&0x1f), byte(pcrPID), 0xf0, 0x00}
	for _, s := range streams {
		body = append(body, s.StreamType, 0xe0|byte((s.PID>>8)&0x1f), byte(s.PID), 0xf0, 0x00)
	}
	sectionLength := 5 + len(body) + 4
	b := sectionHeader(TableIDPMT, program, version, num, last, sectionLength)
	b = append(b, body...)
	return appendCRC(b)
}

func TestPMTMultiSectionOutOfOrder(t *testing.T) {
	tracker := NewPMTTracker()
	s1 := handBuildPMTSection(1, 256, 4, 1, 1, []ElementaryStream{{257, 0x0f, StreamAudio}})
	if pmt, _, err := tracker.Push(patSection(100, 0, s1)); err != nil || pmt != nil {
		t.Fatalf("partial PMT must not commit: pmt=%v err=%v", pmt, err)
	}
	s0 := handBuildPMTSection(1, 256, 4, 0, 1, []ElementaryStream{{256, 0x1b, StreamVideo}})
	pmt, _, err := tracker.Push(patSection(100, 0, s0))
	if err != nil || pmt == nil {
		t.Fatalf("multi-section PMT should complete: %v", err)
	}
	if pmt.Version != 4 || len(pmt.Streams) != 2 || pmt.Streams[0].PID != 256 || pmt.Streams[1].PID != 257 {
		t.Fatalf("merged PMT wrong: %+v", pmt.Streams)
	}
}

func TestPMTRejectsWrongTableID(t *testing.T) {
	tracker := NewPMTTracker()
	sec := BuildPMTSection(1, 256, 0, nil)
	sec[0] = TableIDPAT
	crc := crc32MPEG2(sec[:len(sec)-4])
	sec[len(sec)-4], sec[len(sec)-3], sec[len(sec)-2], sec[len(sec)-1] =
		byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc)
	if _, _, err := tracker.Push(patSection(100, 0, sec)); !errors.Is(err, ErrWrongTableID) {
		t.Fatalf("expected ErrWrongTableID, got %v", err)
	}
}
