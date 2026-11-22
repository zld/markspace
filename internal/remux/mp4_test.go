package remux

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/abema/go-mp4"

	"mediadist/internal/demux"
)

// makeH264Sample builds a synthetic Annex B access unit: SPS, PPS and one
// slice NAL. Bytes are arbitrary but stable so hashes are reproducible.
func makeH264Sample(idr bool, fill int) []byte {
	nals := [][]byte{
		{0x67, 0x42, 0x00, 0x1E, 0xE9},
		{0x68, 0xCE, 0x06, 0xE2},
	}
	slice := make([]byte, 24)
	for i := range slice {
		slice[i] = byte(fill + i)
	}
	if idr {
		slice[0] = 0x65
	} else {
		slice[0] = 0x41
	}
	nals = append(nals, slice)
	var out []byte
	for _, n := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}

func annexBCanon(data []byte) string {
	// Mirror of payloadFor's canonical hash: NAL payloads only, no framing.
	var buf []byte
	off := 0
	for off+4 <= len(data) {
		if !bytes.Equal(data[off:off+4], []byte{0, 0, 0, 1}) {
			break
		}
		start := off + 4
		next := bytes.Index(data[start:], []byte{0, 0, 0, 1})
		end := len(data)
		if next >= 0 {
			end = start + next
		}
		buf = append(buf, data[start:end]...)
		off = end
	}
	sum := sha256.Sum256(buf)
	return fmt.Sprintf("%x", sum[:])
}

func TestWriteFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	var samples []demux.Sample
	for i := 0; i < 6; i++ {
		key := i%3 == 0
		samples = append(samples, demux.Sample{
			Index:    i,
			Stream:   0,
			TrackID:  1,
			Codec:    demux.CodecH264,
			PTS:      int64(i) * 3000,
			Dur:      3000,
			Data:     makeH264Sample(key, i*7+1),
			Keyframe: key,
			Segment:  i / 2,
		})
	}
	plans := []TrackPlan{{
		TrackID:     1,
		Codec:       demux.CodecH264,
		Samples:     samples,
		Timescale:   90000,
		Width:       640,
		Height:      360,
		Handler:     [4]byte{'v', 'i', 'd', 'e'},
		ConfigRecord: []byte{1, 0x42, 0x00, 0x1E, 0xFF, 0xE1, 0x00, 0x04, 0x67, 0x42, 0x00, 0x1E, 0x01, 0x00, 0x02, 0x68, 0xCE},
	}}
	rep, err := WriteFile(dir, "out.mp4", plans, DefaultOptions())
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if rep.SampleCount != len(samples) {
		t.Fatalf("sample count %d want %d", rep.SampleCount, len(samples))
	}
	if len(rep.SHA256) != 64 {
		t.Fatalf("file hash length %d", len(rep.SHA256))
	}

	f, err := os.Open(rep.Path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	// mdhd timescale must match the plan.
	mdhds, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd(),
	})
	if err != nil || len(mdhds) != 1 {
		t.Fatalf("mdhd: %v n=%d", err, len(mdhds))
	}
	mdhd := mdhds[0].Payload.(*mp4.Mdhd)
	if mdhd.Timescale != 90000 {
		t.Fatalf("timescale %d", mdhd.Timescale)
	}

	// stsz sizes must equal the length-prefixed rewrites of each sample.
	stszs, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz(),
	})
	if err != nil || len(stszs) != 1 {
		t.Fatalf("stsz: %v n=%d", err, len(stszs))
	}
	stsz := stszs[0].Payload.(*mp4.Stsz)
	if int(stsz.SampleCount) != len(samples) {
		t.Fatalf("stsz count %d", stsz.SampleCount)
	}

	// mdat bytes must be the stored form, and the canonical per-sample hash
	// must equal the pre-remux canonical hash.
	raw, err := os.ReadFile(rep.Path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for i, ev := range rep.Evidence {
		want := annexBCanon(samples[i].Data)
		if ev.CanonHash != want {
			t.Fatalf("sample %d canon %s want %s", i, ev.CanonHash, want)
		}
		if ev.Size != int(stsz.EntrySize[i]) {
			t.Fatalf("sample %d size %d stsz %d", i, ev.Size, stsz.EntrySize[i])
		}
		got := raw[ev.MP4Offset : ev.MP4Offset+int64(ev.Size)]
		if !bytes.HasPrefix(got, []byte{0, 0, 0, 5}) {
			t.Fatalf("sample %d does not start with a length prefix: %x", i, got[:8])
		}
		// Walk the length prefixes and hash the NAL payloads.
		var nals []byte
		off := 0
		for off+4 <= len(got) {
			n := int(binary.BigEndian.Uint32(got[off:]))
			off += 4
			if off+n > len(got) {
				t.Fatalf("sample %d truncated at %d+%d", i, off, n)
			}
			nals = append(nals, got[off:off+n]...)
			off += n
		}
		sum := sha256.Sum256(nals)
		if fmt.Sprintf("%x", sum[:]) != ev.CanonHash {
			t.Fatalf("sample %d mdat canon mismatch", i)
		}
	}
}

func TestWriteFileRejectsNonMonotonic(t *testing.T) {
	dir := t.TempDir()
	samples := []demux.Sample{
		{Index: 0, TrackID: 1, Codec: demux.CodecAV1, PTS: 100, Dur: 10, Data: []byte{0x0A, 0x01, 0x02}},
		{Index: 1, TrackID: 1, Codec: demux.CodecAV1, PTS: 50, Dur: 10, Data: []byte{0x0A, 0x01, 0x03}},
	}
	plans := []TrackPlan{{TrackID: 1, Codec: demux.CodecAV1, Samples: samples, Timescale: 1000,
		Handler: [4]byte{'v', 'i', 'd', 'e'}}}
	_, err := WriteFile(dir, "bad.mp4", plans, DefaultOptions())
	if err == nil {
		t.Fatal("expected non-monotonic rejection")
	}
	if _, statErr := os.Stat(dir + "/bad.mp4"); statErr == nil {
		t.Fatal("failed write must not leave output")
	}
}
