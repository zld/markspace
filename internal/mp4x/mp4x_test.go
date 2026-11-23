package mp4x

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"

	"mediad/internal/media"
	"mediad/internal/merr"
)

func sampleEntries(n int) []media.IndexEntry {
	es := make([]media.IndexEntry, 0, n)
	for i := 0; i < n; i++ {
		es = append(es, media.IndexEntry{
			Track:         1,
			TimeNs:        int64(i) * 40_000_000,
			ByteOffset:    int64(1_000 + i*900),
			ClusterOffset: int64(i * 500),
		})
	}
	return es
}

func TestVerifyRoundTrip(t *testing.T) {
	entries := sampleEntries(37)
	cc, err := Verify(entries)
	if err != nil {
		t.Fatal(err)
	}
	if cc.Entries != len(entries) {
		t.Fatalf("entries = %d want %d", cc.Entries, len(entries))
	}
	if !cc.ByteIdentical {
		t.Fatal("MP4 output not reproducible byte for byte")
	}
	if cc.Duration == 0 {
		t.Fatal("duration not read back")
	}
	if cc.Digest == "" {
		t.Fatal("empty digest")
	}
}

func TestReopenReadsEveryKeyframe(t *testing.T) {
	entries := sampleEntries(11)
	data, err := Build(entries)
	if err != nil {
		t.Fatal(err)
	}
	nums, dur, err := Reopen(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nums) != 11 {
		t.Fatalf("stss entries = %d want 11", len(nums))
	}
	for i, n := range nums {
		if n != uint32(i+1) {
			t.Fatalf("stss[%d] = %d", i, n)
		}
	}
	// Last entry at 10*40ms = 400ms -> 36000 units at 90 kHz.
	if want := uint64(36_000); dur != want {
		t.Fatalf("duration = %d want %d", dur, want)
	}
}

func TestStcoOffsetsPointInsideMdat(t *testing.T) {
	entries := sampleEntries(9)
	data, err := Build(entries)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)
	mdat, err := mp4.ExtractBox(r, nil, mp4.BoxPath{mp4.BoxTypeMdat()})
	if err != nil || len(mdat) != 1 {
		t.Fatalf("mdat: %v", err)
	}
	payloadStart := mdat[0].Offset + mdat[0].HeaderSize
	payloadEnd := mdat[0].Offset + mdat[0].Size
	stco, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco(),
	})
	if err != nil || len(stco) != 1 {
		t.Fatalf("stco: %v", err)
	}
	offsets := stco[0].Payload.(*mp4.Stco).ChunkOffset
	if len(offsets) != len(entries) {
		t.Fatalf("stco entries = %d want %d", len(offsets), len(entries))
	}
	for i, off := range offsets {
		u := uint64(off)
		if u < payloadStart || u+sampleSize > payloadEnd {
			t.Fatalf("stco[%d] = %d outside mdat payload [%d,%d)", i, off, payloadStart, payloadEnd)
		}
		if want := uint64(offsets[0]) + uint64(i*sampleSize); u != want {
			t.Fatalf("stco[%d] = %d want %d", i, off, want)
		}
	}
}

func TestEmptyIndexRejected(t *testing.T) {
	_, err := Verify(nil)
	if code := merr.CodeOf(err); code != merr.CodeMP4CrossCheck {
		t.Fatalf("code = %s want %s (%v)", code, merr.CodeMP4CrossCheck, err)
	}
}
