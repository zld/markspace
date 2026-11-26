package ingest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/media"
)

func box(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	copy(out[8:], body)
	return out
}

func fullBox(version byte, payload []byte) []byte {
	return append([]byte{version, 0, 0, 0}, payload...)
}

func be32(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.BigEndian.PutUint32(b[4*i:], x)
	}
	return b
}

func be16(v ...uint16) []byte {
	b := make([]byte, 2*len(v))
	for i, x := range v {
		binary.BigEndian.PutUint16(b[2*i:], x)
	}
	return b
}

func testAACMP4(t *testing.T, sizes []uint32) []byte {
	t.Helper()
	ftyp := box("ftyp", []byte("M4A "), be32(0), []byte("M4A "), []byte("isom"))

	mvhdPayload := bytes.Join([][]byte{
		be32(0, 0, 44100, uint32(len(sizes)*1024)),
		be32(0x00010000),
		be16(0x0100),
		make([]byte, 10),
		{0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
			0, 0, 0, 0, 0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
			0x40, 0, 0, 0},
		make([]byte, 24),
		be32(2),
	}, nil)
	mvhd := box("mvhd", fullBox(0, mvhdPayload))

	tkhdPayload := bytes.Join([][]byte{
		be32(0, 0, 1, 0, uint32(len(sizes)*1024)),
		make([]byte, 8),
		be16(0, 0, 0, 0),
		{0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
			0, 0, 0, 0, 0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
			0x40, 0, 0, 0},
		be32(0, 0),
	}, nil)
	tkhd := box("tkhd", fullBox(0, tkhdPayload))

	mdhdPayload := append(be32(0, 0, 44100, uint32(len(sizes)*1024)), be16(0x55c4, 0)...)
	mdhd := box("mdhd", fullBox(0, mdhdPayload))
	hdlrPayload := bytes.Join([][]byte{be32(0), []byte("soun"), be32(0, 0, 0), []byte("AAC\x00")}, nil)
	hdlr := box("hdlr", fullBox(0, hdlrPayload))

	stts := box("stts", fullBox(0, bytes.Join([][]byte{be32(1), be32(uint32(len(sizes)), 1024)}, nil)))
	stsc := box("stsc", fullBox(0, append(be32(1), be32(1, uint32(len(sizes)), 1)...)))
	stszPayload := append(be32(0, uint32(len(sizes))), make([]byte, 4*len(sizes))...)
	for i, size := range sizes {
		binary.BigEndian.PutUint32(stszPayload[8+4*i:], size)
	}
	stsz := box("stsz", fullBox(0, stszPayload))
	stcoZero := box("stco", fullBox(0, append(be32(1), be32(0)...)))
	stbl := box("stbl", stts, stsc, stsz, stcoZero)
	smhd := box("smhd", fullBox(0, append(be16(0), be16(0)...)))
	minf := box("minf", smhd, stbl)
	mdia := box("mdia", mdhd, hdlr, minf)
	trak := box("trak", tkhd, mdia)
	moov := box("moov", mvhd, trak)

	payload := make([]byte, 0)
	for _, size := range sizes {
		payload = append(payload, make([]byte, int(size))...)
	}
	dataOffset := uint32(len(ftyp) + len(moov) + 8)
	stco := box("stco", fullBox(0, append(be32(1), be32(dataOffset)...)))
	moov = bytes.Replace(moov, stcoZero, stco, 1)
	mdat := box("mdat", payload)
	return bytes.Join([][]byte{ftyp, moov, mdat}, nil)
}

func writeAACFixture(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aac.mp4"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadAACSampleDurationsAndOffsets(t *testing.T) {
	sizes := []uint32{11, 13, 17, 19, 23, 29}
	data := testAACMP4(t, sizes)
	dir := writeAACFixture(t, data)
	got, err := ReadAACTrack(TrackSpec{ID: "aac", Kind: KindAAC, Path: "aac.mp4"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Samples) != 6 {
		t.Fatalf("samples=%d", len(got.Samples))
	}
	var wantOffset int64
	for i, s := range got.Samples {
		if i == 0 {
			if s.Offset <= 0 {
				t.Fatalf("sample 0 has non-positive offset %d", s.Offset)
			}
			wantOffset = s.Offset
		}
		if s.Index != i {
			t.Fatalf("sample index=%d want %d", s.Index, i)
		}
		if s.DurationUnits != 1024 || s.DurationTicks != 1024*160 {
			t.Fatalf("sample %d duration=%d/%d", i, s.DurationUnits, s.DurationTicks)
		}
		if s.Offset != wantOffset {
			t.Fatalf("sample %d offset=%d want %d", i, s.Offset, wantOffset)
		}
		wantOffset = s.Offset + int64(s.Size)
	}
	if got.Samples[5].Offset+int64(got.Samples[5].Size) != int64(len(data)) {
		t.Fatalf("end=%d file=%d", got.Samples[5].Offset+int64(got.Samples[5].Size), len(data))
	}
}

func TestTruncatedAACReportsOffset(t *testing.T) {
	sizes := []uint32{11, 13, 17, 19, 23, 29}
	data := testAACMP4(t, sizes)
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.mp4")
	if err := os.WriteFile(path, data[:len(data)-20], 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadAACTrack(TrackSpec{ID: "aac-bad", Kind: KindAAC, Path: "truncated.mp4"}, dir)
	var fe *media.Error
	if !errors.As(err, &fe) || fe.Kind != media.KindInput || fe.Phase != "ingest.mp4" {
		t.Fatalf("expected MP4 input fault, got %v", err)
	}
	if fe.ByteOffset == nil || *fe.ByteOffset <= 0 || fe.RecordIndex == nil {
		t.Fatalf("missing offset/record: %+v", fe)
	}
	if media.ExitCode(err) != 2 {
		t.Fatalf("exit=%d", media.ExitCode(err))
	}
}
