package index

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"mediaroute/internal/clock"
	"mediaroute/internal/codec"
)

func TestKidxRoundTrip(t *testing.T) {
	entries := []Entry{NewEntry(1), NewEntry(4), NewEntry(9)}
	buf := newSeekBuffer()
	if err := WriteMP4(buf, entries); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("kidx")) {
		t.Fatal("MP4 framing missing kidx box type")
	}
	got, err := ReadMP4(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(entries) {
		t.Fatalf("entry count = %d want %d", len(got), len(entries))
	}
	for i := range entries {
		if got[i] != entries[i] {
			t.Fatalf("entry %d changed: %+v -> %+v", i, entries[i], got[i])
		}
	}
}

func TestValidateHappyPath(t *testing.T) {
	entries := []Entry{NewEntry(2), NewEntry(5)}
	samples := map[uint32]bool{2: true, 5: true}
	if err := Validate(codec.H264, 1, entries, samples); err != nil {
		t.Fatalf("valid index rejected: %v", err)
	}
	for _, c := range []codec.Codec{codec.H265, codec.AV1} {
		if err := Validate(c, 1, entries, samples); err != nil {
			t.Fatalf("codec %s rejected: %v", c, err)
		}
	}
}

func TestValidateCorruptChecksum(t *testing.T) {
	bad := NewEntry(3)
	bad.CRC ^= 0xDEADBEEF
	err := Validate(codec.H264, 7, []Entry{bad}, map[uint32]bool{3: true})
	if err == nil {
		t.Fatal("corrupt checksum accepted")
	}
	pe, ok := clock.AsError(err)
	if !ok || pe.Recoverable || pe.Track != 7 || pe.Phase != clock.PhaseIndex {
		t.Fatalf("corruption error model wrong: %v", err)
	}
	if !strings.Contains(err.Error(), "refusing to repair") {
		t.Fatalf("error must state no silent repair: %v", err)
	}
}

func TestValidateDanglingAndOrder(t *testing.T) {
	// Entry points at a sample that is not a complete keyframe.
	err := Validate(codec.H264, 1, []Entry{NewEntry(4)}, map[uint32]bool{2: true})
	if err == nil {
		t.Fatal("dangling index entry accepted")
	}
	// Non-monotonic entries.
	samples := map[uint32]bool{1: true, 2: true}
	err = Validate(codec.H264, 1, []Entry{NewEntry(2), NewEntry(1)}, samples)
	if err == nil {
		t.Fatal("out-of-order entries accepted")
	}
	// Zero sample.
	if err = Validate(codec.H264, 1, []Entry{NewEntry(0)}, samples); err == nil {
		t.Fatal("zero sample accepted")
	}
}

func TestValidateUnsupportedCodec(t *testing.T) {
	err := Validate(codec.Codec("vp8"), 1, nil, nil)
	if err == nil {
		t.Fatal("unknown codec accepted for index validation")
	}
}

func TestReadTruncatedIndex(t *testing.T) {
	entries := []Entry{NewEntry(1)}
	buf := newSeekBuffer()
	if err := WriteMP4(buf, entries); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	// Cut the file mid-payload; the declared box size cannot be satisfied.
	truncated := raw[:len(raw)-3]
	if _, err := ReadMP4(bytes.NewReader(truncated)); err == nil {
		t.Fatal("truncated index file accepted")
	}
}

// seekBuffer is a byte buffer that also satisfies io.WriteSeeker for go-mp4.
type seekBuffer struct {
	data []byte
	pos  int64
}

func newSeekBuffer() *seekBuffer { return &seekBuffer{} }

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := int(s.pos) + len(p)
	if end > len(s.data) {
		s.data = append(s.data, make([]byte, end-len(s.data))...)
	}
	copy(s.data[s.pos:end], p)
	s.pos = int64(end)
	return len(p), nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	var pos int64
	switch whence {
	case 0:
		pos = offset
	case 1:
		pos = s.pos + offset
	case 2:
		pos = int64(len(s.data)) + offset
	}
	if pos < 0 {
		return 0, errors.New("negative seek")
	}
	s.pos = pos
	return pos, nil
}

func (s *seekBuffer) Bytes() []byte { return s.data }
