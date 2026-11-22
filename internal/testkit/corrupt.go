package testkit

import (
	"encoding/binary"
	"os"

	"mediacore/internal/clock"
	"mediacore/internal/scan"
)

// Corruption identifies the five fixture scenarios required by the spec.
type Corruption string

const (
	CorruptTruncated   Corruption = "truncated"
	CorruptEditOOB     Corruption = "edit_oob"
	CorruptWrap        Corruption = "wrap"
	CorruptIndexBroken Corruption = "index_broken"
	CorruptNoKeyframe  Corruption = "no_keyframe"
)

// TruncateMP4 cuts the file mid-mdat so the last sample's declared extent
// runs past EOF. The manifest still describes the original sample count, so
// truncation propagates through indexing and into the stream stage.
func TruncateMP4(path string, bytesToDrop int64) error {
	fi, err := os.Stat(path)
	if err != nil {
		return clock.ErrRecoverablef("testkit.corrupt", "stat: %v", err)
	}
	if bytesToDrop <= 0 || bytesToDrop >= fi.Size() {
		return clock.ErrInvalidArgument("testkit: truncation must drop 0 < n < filesize")
	}
	return os.Truncate(path, fi.Size()-bytesToDrop)
}

// TruncateSampleExtent models network truncation that survives structural
// validation: the mdat header is rewritten with size=0 ("extends to EOF"),
// so the box hierarchy stays internally consistent, while the trailing
// sample extents in stco/stsz now run past the shortened mdat. The failure
// therefore surfaces during index construction / stream reads (sample-level
// bounds), not at the box walk.
func TruncateSampleExtent(path string, bytesToDrop int64) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return clock.ErrRecoverablef("testkit.corrupt", "read: %v", err)
	}
	if bytesToDrop <= 0 || int64(len(data)) <= bytesToDrop {
		return clock.ErrInvalidArgument("testkit: truncation must drop 0 < n < filesize")
	}
	mdatOff, err := findBoxOffset(data, "mdat")
	if err != nil {
		return err
	}
	// size=0: box occupies the remainder of the file after truncation.
	binary.BigEndian.PutUint32(data[mdatOff:mdatOff+4], 0)
	shortened := data[:len(data)-int(bytesToDrop)]
	return os.WriteFile(path, shortened, 0o644)
}

// findBoxOffset does a minimal top-level box scan to locate one box type.
func findBoxOffset(data []byte, want string) (int64, error) {
	off := 0
	for off+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		hdr := 8
		if size == 1 {
			if off+16 > len(data) {
				return 0, clock.ErrRecoverablef("testkit.corrupt", "64-bit box too short")
			}
			size = int(binary.BigEndian.Uint64(data[off+8 : off+16]))
			hdr = 16
		}
		if size == 0 {
			size = len(data) - off
		}
		if typ == want {
			return int64(off), nil
		}
		off += size
		_ = hdr
	}
	return 0, clock.ErrRecoverablef("testkit.corrupt", "box %q not found", want)
}

// WriteEditOutOfBounds emits an edit list whose media_time starts beyond the
// track duration. The MP4 is generated twice: the second write carries the
// excessive edit-list origin while the manifest is generated from the
// original (valid) spec first, so the mismatch is the edit list alone. The
// scanner's edit resolver must reject it and the failure propagates to the
// manifest stage.
func WriteEditOutOfBounds(spec MP4Spec) error {
	if err := WriteMP4(spec); err != nil {
		return err
	}
	if _, err := WriteManifest(spec); err != nil {
		return err
	}
	bad := spec
	bad.MediaStartTicks = int64(spec.SampleCount)*int64(spec.SampleDelta) + 90000
	// Keep the original (short) mdhd duration so the edit window genuinely
	// exceeds media duration.
	bad.ForceMdhdDuration = uint32(spec.SampleCount * spec.SampleDelta)
	// Overwrite only the MP4; the manifest keeps the valid original window.
	return WriteMP4(bad)
}

// WriteBrokenIndex creates an index-cache file whose bytes look plausible
// (correct magic and length for the record count) but whose trailer hash is
// wrong, forcing load verification to fail and the runner to rebuild.
func WriteBrokenIndex(path string, hdrLike []byte, recordCount uint32) error {
	// hdrLike is at least 64 bytes of header; fill records with zeros and
	// append a deliberately wrong trailer.
	body := make([]byte, 64+int(recordCount)*44)
	copy(body, hdrLike)
	trailer := make([]byte, 32)
	for i := range trailer {
		trailer[i] = 0xa5
	}
	data := append(body, trailer...)
	return os.WriteFile(path, data, 0o644)
}

// allSampleNumbers returns the 1-based number of every sample.
func allSampleNumbers(count uint32) []uint32 {
	out := make([]uint32, count)
	for i := range out {
		out[i] = uint32(i + 1)
	}
	return out
}

// ManifestForNoKeyframe forces every sync flag off, so a random access past
// sample zero has no decodable anchor.
func ManifestForNoKeyframe(m *scan.Manifest) *scan.Manifest {
	cp := *m
	cp.ForceNonSync = allSampleNumbers(m.SampleCount)
	return &cp
}

// CorruptBoxLength writes a top-level box whose declared size extends beyond
// EOF, exercising the structural validator's illegal-length path. The file
// is synthesized from an otherwise valid fixture.
func CorruptBoxLength(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return clock.ErrRecoverablef("testkit.corrupt", "read: %v", err)
	}
	if len(data) < 8 {
		return clock.ErrInvalidArgument("testkit: file too small to corrupt")
	}
	// Inflate the first box's declared size beyond the file.
	binary.BigEndian.PutUint32(data[0:4], uint32(len(data))+4096)
	return os.WriteFile(path, data, 0o644)
}
