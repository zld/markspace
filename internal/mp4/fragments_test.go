package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"

	"mediatool/internal/timeline"
)

// fullBox prepends version+flags to a full-box payload.
func fullBox(ver uint8, flags uint32, rest []byte) []byte {
	out := make([]byte, 4+len(rest))
	out[0] = ver
	out[1] = byte(flags >> 16)
	out[2] = byte(flags >> 8)
	out[3] = byte(flags)
	copy(out[4:], rest)
	return out
}

func full(typ string, body []byte) []byte { return box(typ, body) }

// buildTraf constructs a traf with tfhd, tfdt(v1) and one trun describing
// sample sizes/durations; mdat ordering follows the traf order.
func buildTraf(trackID uint32, baseDecode uint64, sizes, durations []uint32) []byte {
	tfhd := full("tfhd", fullBox(0, 0x020000, u32(trackID))) // default-base-is-moof
	var tfdtRest [8]byte
	binary.BigEndian.PutUint64(tfdtRest[:], baseDecode)
	tfdt := full("tfdt", fullBox(1, 0, tfdtRest[:]))

	flags := trunDataOffset | trunSampleDuration | trunSampleSize
	trunBody := make([]byte, 8)
	binary.BigEndian.PutUint32(trunBody[0:4], flags)
	binary.BigEndian.PutUint32(trunBody[4:8], uint32(len(sizes)))
	trunBody = append(trunBody, u32(8)...) // data-offset placeholder
	for i := range sizes {
		trunBody = append(trunBody, u32(durations[i])...)
		trunBody = append(trunBody, u32(sizes[i])...)
	}
	trun := full("trun", trunBody)
	return box("traf", append(append(tfhd, tfdt...), trun...))
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// buildFragment wraps moof(mfhd, trafs...) + mdat(payload).
func buildFragment(seq int, trafs [][]byte, mdat []byte) []byte {
	mfhd := full("mfhd", fullBox(0, 0, u32(uint32(seq))))
	moofInner := mfhd
	for _, tr := range trafs {
		moofInner = append(moofInner, tr...)
	}
	moof := box("moof", moofInner)
	return append(moof, box("mdat", mdat)...)
}

func TestParseFragmentHappyPath(t *testing.T) {
	mdat := []byte("sample-one-bytes!") // 17 bytes: 10 + 7
	sizes := []uint32{10, 7}
	durations := []uint32{3003, 3003}
	traf := buildTraf(1, 90000, sizes, durations)
	data := buildFragment(3, [][]byte{traf}, mdat)

	frag, err := ParseFragment(bytes.NewReader(data), 0)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if frag.Rejected {
		t.Fatalf("fragment rejected: %+v", frag.Errors)
	}
	if frag.Seq != 3 {
		t.Fatalf("seq = %d, want 3", frag.Seq)
	}
	if len(frag.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(frag.Runs))
	}
	run := frag.Runs[0]
	if run.TrackID != 1 || !run.TfdtPresent || run.BaseMediaDecode != 90000 {
		t.Fatalf("unexpected run header: %+v", run)
	}
	if len(run.SampleSizes) != 2 || run.SampleSizes[0] != 10 || run.SampleDurations[1] != 3003 {
		t.Fatalf("trun samples wrong: %+v / %+v", run.SampleSizes, run.SampleDurations)
	}
	if !bytes.Equal(frag.MdatPayload, mdat) {
		t.Fatal("mdat payload not preserved")
	}
	b0, h0, err := frag.SamplePayload(0, 0)
	if err != nil || string(b0) != "sample-one" {
		t.Fatalf("sample 0 slice wrong: %q err=%v", b0, err)
	}
	b1, h1, err := frag.SamplePayload(0, 1)
	if err != nil || string(b1) != "-bytes!" {
		t.Fatalf("sample 1 slice wrong: %q err=%v", b1, err)
	}
	if h0 == h1 {
		t.Fatal("distinct samples must have distinct hashes")
	}
	all, ph, err := frag.RunPayload(0)
	if err != nil || string(all) != string(mdat) {
		t.Fatalf("run payload wrong: %q err=%v", all, err)
	}
	if ph != sha256Of(mdat) {
		t.Fatal("run payload hash mismatch")
	}
}

func TestParseFragmentMissingMfhd(t *testing.T) {
	traf := buildTraf(2, 0, []uint32{4}, []uint32{1024})
	mfhdLess := box("moof", traf)
	data := append(mfhdLess, box("mdat", []byte("abcd"))...)
	frag, err := ParseFragment(bytes.NewReader(data), 0)
	if err != nil || frag.Rejected {
		t.Fatalf("missing mfhd must be recoverable: err=%v rejected=%v", err, frag.Rejected)
	}
	if frag.Seq != -1 {
		t.Fatalf("missing mfhd must yield Seq=-1, got %d", frag.Seq)
	}
	if frag.Runs[0].TrackID != 2 {
		t.Fatal("traf after missing mfhd must still parse")
	}
}

func TestParseFragmentTruncatedBox(t *testing.T) {
	// Valid start, then a header claiming 64 bytes with only 4 payload bytes.
	data := buildFragment(1,
		[][]byte{buildTraf(1, 0, []uint32{2}, []uint32{100})},
		[]byte("ab"))
	data = append(data, 0x00, 0x00, 0x00, 0x40, 't', 'r', 'u', 'n', 1, 2, 3, 4)
	_, err := ParseFragment(bytes.NewReader(data), 0)
	me := timeline.AsMediaError(err)
	if me == nil || me.Class != timeline.ClassBoxTruncated {
		t.Fatalf("want truncated-box error, got %v", err)
	}
}

func TestParseFragmentMissingMdat(t *testing.T) {
	mfhd := full("mfhd", fullBox(0, 0, u32(1)))
	data := box("moof", append(mfhd, buildTraf(1, 0, []uint32{1}, []uint32{1})...))
	frag, err := ParseFragment(bytes.NewReader(data), 0)
	if err != nil {
		t.Fatalf("missing mdat is not a scan error: %v", err)
	}
	if !frag.Rejected || len(frag.Errors) == 0 ||
		frag.Errors[0].Class != timeline.ClassFieldMissing {
		t.Fatalf("want rejected fragment with field-missing, got %+v", frag.Errors)
	}
}

func TestParseFragmentTrunEntryTruncated(t *testing.T) {
	// trun claims 3 samples but carries entries for only one.
	tfhd := full("tfhd", fullBox(0, 0x020000, u32(7)))
	trunBody := make([]byte, 8)
	binary.BigEndian.PutUint32(trunBody[0:4], trunSampleDuration|trunSampleSize)
	binary.BigEndian.PutUint32(trunBody[4:8], 3)
	trunBody = append(trunBody, u32(100)...)
	trunBody = append(trunBody, u32(4)...)
	traf := box("traf", append(tfhd, full("trun", trunBody)...))
	data := buildFragment(9, [][]byte{traf}, []byte("abcd"))
	frag, err := ParseFragment(bytes.NewReader(data), 0)
	if err != nil {
		t.Fatalf("trun truncation should be recorded, not fatal: %v", err)
	}
	found := false
	for _, e := range frag.Errors {
		if e.Class == timeline.ClassBoxTruncated {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected truncated trun-entry error, got %+v", frag.Errors)
	}
	if frag.Runs[0].SampleCount() != 3 {
		t.Fatal("declared sample count must still be retained for diagnostics")
	}
}

func TestParseFragmentMultiTraf(t *testing.T) {
	video := buildTraf(1, 0, []uint32{5}, []uint32{3003})
	audio := buildTraf(2, 0, []uint32{3, 2}, []uint32{1024, 1024})
	data := buildFragment(4, [][]byte{video, audio}, []byte("videoxaud"))
	frag, err := ParseFragment(bytes.NewReader(data), 0)
	if err != nil || frag.Rejected {
		t.Fatalf("multi-traf parse failed: %v errs=%+v", err, frag.Errors)
	}
	if len(frag.Runs) != 2 || frag.Runs[0].TrackID != 1 || frag.Runs[1].TrackID != 2 {
		t.Fatalf("multi-traf runs wrong: %+v", frag.Runs)
	}
}

func TestSampleSliceBeyondMdat(t *testing.T) {
	traf := buildTraf(1, 0, []uint32{100}, []uint32{1})
	data := buildFragment(1, [][]byte{traf}, []byte("short"))
	frag, err := ParseFragment(bytes.NewReader(data), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := frag.SamplePayload(0, 0); timeline.AsMediaError(err).Class != timeline.ClassBoxTruncated {
		t.Fatalf("want truncation error for oversize sample, got %v", err)
	}
}
