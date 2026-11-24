package mediacore

import (
	"encoding/binary"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// truncateMoov copies src to dst with the final 24 bytes removed. The moov
// box header still claims its original size, so the demuxer must detect a
// truncated container rather than trusting the header.
func truncateMoov(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return wrapErr(KindIO, "fixtures", err)
	}
	if len(data) < 1024 {
		return fail(KindInternal, "fixtures", "fixture too small to truncate")
	}
	cut := len(data) - 24
	if err := os.WriteFile(dst, data[:cut], 0o644); err != nil {
		return wrapErr(KindIO, "fixtures", err)
	}
	return nil
}

// corruptChunkOffset rewrites the first stco entry of the first track to a
// value far outside any mdat payload.
func corruptChunkOffset(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return wrapErr(KindIO, "fixtures", err)
	}
	f := bytesSeeker(data)
	moovs, err := mp4.ExtractBoxes(f, nil, []mp4.BoxPath{{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()}})
	if err != nil {
		return wrapErr(KindInternal, "fixtures", err)
	}
	if len(moovs) == 0 {
		return fail(KindInternal, "fixtures", "no stco box in fixture")
	}
	stco := moovs[0]
	// Payload layout: full box (4) + entry_count (4) + first offset (4).
	pos := int64(stco.Offset+stco.HeaderSize) + 8
	if pos+4 > int64(len(data)) {
		return fail(KindInternal, "fixtures", "stco entry out of range")
	}
	binary.BigEndian.PutUint32(data[pos:pos+4], 0x7FF00000)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return wrapErr(KindIO, "fixtures", err)
	}
	return nil
}

// mutateMediaByte flips one byte inside the first mdat payload, changing the
// input digest without invalidating the container structure.
func mutateMediaByte(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return wrapErr(KindIO, "fixtures", err)
	}
	f := bytesSeeker(data)
	mdats, err := mp4.ExtractBoxes(f, nil, []mp4.BoxPath{{mp4.BoxTypeMdat()}})
	if err != nil {
		return wrapErr(KindInternal, "fixtures", err)
	}
	if len(mdats) == 0 {
		return fail(KindInternal, "fixtures", "no mdat in fixture")
	}
	pos := int64(mdats[0].Offset+mdats[0].HeaderSize) + 3
	if pos >= int64(len(data)) {
		return fail(KindInternal, "fixtures", "mdat payload too small")
	}
	data[pos] ^= 0x5a
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return wrapErr(KindIO, "fixtures", err)
	}
	return nil
}
