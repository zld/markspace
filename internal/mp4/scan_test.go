package mp4

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/fixture"
)

// shortErr and itoa keep the offset assertions dependency-free.
type shortErr struct{ msg string }

func (e shortErr) Error() string { return e.msg }

func errShort(msg string, _ int64) error { return shortErr{msg: msg} }

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		p--
		b[p] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

// generate writes a fixture set and returns its root.
func generate(t *testing.T, corrupt string) *fixture.Session {
	t.Helper()
	dir := t.TempDir()
	sess, err := fixture.Generate(dir, 42, 8_000_000, corrupt)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return sess
}

func TestScanCleanMedia(t *testing.T) {
	sess := generate(t, fixture.CorruptNone)
	s, err := ScanFile(filepath.Join(sess.Root, "media", "cam-a.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("tracks=%d", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if tr.TrackID != 1 || tr.Kind != "video" || tr.Timescale != 90000 {
		t.Fatalf("unexpected track: %+v", tr)
	}
	if len(tr.Samples) != 360 {
		t.Fatalf("samples=%d", len(tr.Samples))
	}
	if !tr.HasSyncTable {
		t.Fatal("expected stss")
	}
	// Offsets must be inside the file and non-overlapping.
	prevEnd := uint64(0)
	for i, smp := range tr.Samples {
		if smp.Offset < prevEnd {
			t.Fatalf("sample %d overlaps previous", i)
		}
		if int64(smp.Offset)+int64(smp.Size) > s.Size {
			t.Fatalf("sample %d extends past EOF", i)
		}
		prevEnd = smp.Offset + uint64(smp.Size)
		if smp.TableOffset <= 0 {
			t.Fatalf("sample %d missing table offset", i)
		}
	}
	// 90 s of frames at 90 kHz is 1.08e6 ticks; duration must be plausible.
	if tr.DurationTicks <= 0 {
		t.Fatalf("duration=%d", tr.DurationTicks)
	}
	if tr.EditList.FirstMediaTime() != 0 {
		t.Fatalf("cam-a should start at 0, got %d", tr.EditList.FirstMediaTime())
	}
}

func TestScanPreservesNonzeroOrigin(t *testing.T) {
	sess := generate(t, fixture.CorruptNone)
	s, err := ScanFile(filepath.Join(sess.Root, "media", "cam-b.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	tr := s.Tracks[0]
	if got := tr.EditList.FirstMediaTime(); got == 0 {
		t.Fatal("cam-b should preserve a nonzero elst media_time")
	}
	// cam-b runs at 25 fps (3600-tick frames); press-start is one frame.
	if tr.EditList.FirstMediaTime() != 3600 {
		t.Fatalf("elst media_time=%d want 3600", tr.EditList.FirstMediaTime())
	}
}

func TestScanTruncatedTableReportsOffset(t *testing.T) {
	sess := generate(t, fixture.CorruptTruncatedTable)
	_, err := ScanFile(filepath.Join(sess.Root, "media", "cam-a.mp4"))
	if err == nil {
		t.Fatal("expected truncation error")
	}
	pe, ok := clock.As(err)
	if !ok {
		t.Fatalf("unstructured error: %v", err)
	}
	if pe.Stage != clock.StageScan || pe.Code != clock.CodeTruncatedTable {
		t.Fatalf("stage/code = %s/%s", pe.Stage, pe.Code)
	}
	if pe.RawOffset < 0 {
		t.Fatalf("missing raw offset: %+v", pe)
	}
	// Verify the reported offset really holds the stco box header.
	if err := checkBoxAt(sess.Root, pe.RawOffset, "stco"); err != nil {
		t.Fatal(err)
	}
}

func checkBoxAt(root string, off int64, want string) error {
	data, err := os.ReadFile(filepath.Join(root, "media", "cam-a.mp4"))
	if err != nil {
		return err
	}
	if int(off)+8 > len(data) {
		return errShort(want, off)
	}
	if !bytes.Equal(data[off+4:off+8], []byte(want)) {
		return errShort(want+" mismatch at "+itoa(off), off)
	}
	return nil
}

func TestScanRejectsAbsurdDeclaredSizes(t *testing.T) {
	sess := generate(t, fixture.CorruptNone)
	path := filepath.Join(sess.Root, "media", "cam-a.mp4")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Inflate the mvhd box size to exceed the file. Find "mvhd" and rewrite the
	// 4 bytes preceding it.
	idx := bytes.Index(data, []byte("mvhd"))
	if idx < 4 {
		t.Fatal("mvhd not found")
	}
	data[idx-4] = 0x7f
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ScanFile(path)
	pe, ok := clock.As(err)
	if !ok || pe.Code != clock.CodeTruncatedTable {
		t.Fatalf("expected truncated_table, got %v", err)
	}
	if pe.RawOffset != int64(idx-4) {
		t.Fatalf("offset=%d want %d", pe.RawOffset, idx-4)
	}
}

func TestScanMissingSyncFlags(t *testing.T) {
	sess := generate(t, fixture.CorruptMissingSync)
	s, err := ScanFile(filepath.Join(sess.Root, "media", "cam-a.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	tr := s.Tracks[0]
	if tr.HasSyncTable {
		t.Fatal("expected no stss")
	}
	found := false
	for _, p := range tr.Problems {
		if p.Code == clock.CodeMissingSyncFlags {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing_sync_flags not reported: %+v", tr.Problems)
	}
	for _, smp := range tr.Samples {
		if smp.Sync {
			t.Fatal("no sample can be sync when stss is absent")
		}
	}
}

func TestOversizedStszRejectedBeforeAllocation(t *testing.T) {
	sess := generate(t, fixture.CorruptNone)
	path := filepath.Join(sess.Root, "media", "cam-a.mp4")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// stsz: type(4) then version/flags(4) sample_size(4) sample_count(4).
	pos := bytes.Index(data, []byte("stsz"))
	if pos < 0 {
		t.Fatal("stsz not found")
	}
	countOff := pos + 4 + 8
	if countOff+4 > len(data) {
		t.Fatal("stsz too small")
	}
	data[countOff] = 0x02
	data[countOff+1] = 0xFA
	data[countOff+2] = 0xF0
	data[countOff+3] = 0x80 // ~50M samples
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ScanFile(path)
	pe, ok := clock.As(err)
	if !ok {
		t.Fatalf("unstructured: %v", err)
	}
	if pe.Code != clock.CodeOversizedTable && pe.Code != clock.CodeTruncatedTable {
		t.Fatalf("code=%s", pe.Code)
	}
	if pe.RawOffset <= 0 {
		t.Fatalf("missing offset: %+v", pe)
	}
}
