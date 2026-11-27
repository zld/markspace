package container

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	merr "mediatool/internal/errors"
)

// patchStco replaces the placeholder chunk offset with the mdat payload
// offset. Layout inside the stco box: 8 header + 4 fullbox + 4 entry_count +
// 4 chunk_offset.
func patchStco(data []byte) {
	mdatSig := []byte("mdat")
	mdatAt := bytes.Index(data, mdatSig)
	if mdatAt < 0 {
		panic("mdat not found")
	}
	// mdatAt points at the type field; 4 size bytes precede it, so payload
	// starts at mdatAt+4.
	chunkOffset := uint32(mdatAt + 4)
	stcoAt := bytes.Index(data, []byte("stco"))
	if stcoAt < 0 {
		panic("stco not found")
	}
	at := stcoAt + 4 + 4 + 4 // skip type, fullbox header, entry count
	binary.BigEndian.PutUint32(data[at:], chunkOffset)
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanValidAudioMP4(t *testing.T) {
	data := buildAudioMP4(48000, 10, 4800, 1920)
	patchStco(data)
	p := writeTemp(t, "ok.mov", data)

	f, err := Scan(p)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if f.Timescale != 1000 {
		t.Fatalf("movie timescale = %d", f.Timescale)
	}
	tr := f.AudioTrack()
	if tr == nil || tr.Timescale != 48000 || tr.Kind != KindAudio {
		t.Fatalf("audio track not found: %+v", tr)
	}
	if len(tr.Samples) != 10 {
		t.Fatalf("samples = %d, want 10", len(tr.Samples))
	}
	// 4800 ticks @ 48kHz = 100 ms per sample.
	if tr.Samples[1].PTS != 100_000_000 || tr.Samples[1].Duration != 100_000_000 {
		t.Fatalf("sample timing wrong: %+v", tr.Samples[1])
	}
	if tr.Samples[0].FileOffset != int64(f.MdatOffset) {
		t.Fatalf("first offset %d != mdat payload %d", tr.Samples[0].FileOffset, f.MdatOffset)
	}
	// Consecutive samples pack tightly in the single chunk.
	if tr.Samples[2].FileOffset != f.MdatOffset+2*1920 {
		t.Fatalf("sample 2 offset wrong: %d", tr.Samples[2].FileOffset)
	}
	tl, err := tr.Timeline()
	if err != nil || tl.Len() != 10 {
		t.Fatalf("timeline: %v len=%d", err, tl.Len())
	}
}

func TestScanTruncatedReportsOffset(t *testing.T) {
	data := buildAudioMP4(48000, 4, 4800, 1920)
	patchStco(data)

	// Cut the file inside mdat so a sample range crosses EOF.
	cut := len(data) - 100
	p := writeTemp(t, "trunc.mov", data[:cut])
	_, err := Scan(p)
	if err == nil {
		t.Fatal("truncated file accepted")
	}
	var me *merr.MediaError
	if !merr.As(err, &me) || me.Offset < 0 {
		t.Fatalf("expected offset-bearing MediaError, got %v", err)
	}
	if code := merr.AsCode(err); code != merr.CodeTruncated && code != merr.CodeBoxOutOfRange {
		t.Fatalf("unexpected code %s", code)
	}
	if code := merr.AsCode(err); code.ExitCode() != 2 {
		t.Fatalf("truncation must map to exit code 2, got %d", code.ExitCode())
	}
}

func TestScanBoxLengthOutOfRange(t *testing.T) {
	data := buildAudioMP4(48000, 2, 4800, 1920)
	patchStco(data)
	// Corrupt the moov box declared length so it extends past EOF.
	moovAt := bytes.Index(data, []byte("moov"))
	binary.BigEndian.PutUint32(data[moovAt-4:], uint32(len(data)+5000))
	p := writeTemp(t, "badbox.mov", data)
	_, err := Scan(p)
	if err == nil {
		t.Fatal("oversized box accepted")
	}
	var me *merr.MediaError
	if !merr.As(err, &me) || me.Offset < 0 {
		t.Fatalf("expected offset-bearing MediaError, got %v", err)
	}
	switch merr.AsCode(err) {
	case merr.CodeBoxOutOfRange, merr.CodeTruncated, merr.CodeInvalidBox:
	default:
		t.Fatalf("unexpected code %s", merr.AsCode(err))
	}
}

func TestScanTruncatedSampleTable(t *testing.T) {
	data := buildAudioMP4(48000, 3, 4800, 1920)
	patchStco(data)
	// Locate stsz and shrink its declared sample count by zeroing one entry
	// size is hard; instead truncate right after the moov header start is
	// unreliable. Replace one stsz entry size field with an inconsistent
	// count by trimming the file at the stco payload boundary.
	stcoSig := []byte("stco")
	at := bytes.Index(data, stcoSig)
	// Cut inside the stco box payload (8-byte box header + 12 fullbox/entry).
	p := writeTemp(t, "table.mov", data[:at+4])
	_, err := Scan(p)
	if err == nil {
		t.Fatal("truncated sample table accepted")
	}
	if merr.AsCode(err).ExitCode() != 2 {
		t.Fatalf("table corruption exit code = %d", merr.AsCode(err).ExitCode())
	}
}
