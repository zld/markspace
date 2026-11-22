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
	CorruptTruncated    Corruption = "truncated"
	CorruptEditOOB      Corruption = "edit_oob"
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

// WriteEditOutOfBounds emits an edit list whose media_time starts beyond the
// track duration. It is written as a standalone replacement asset; the
// scanner's edit resolver must reject it and the failure propagates to the
// manifest stage.
func WriteEditOutOfBounds(spec MP4Spec) error {
	// First generate a valid asset, then rewrite with an excessive origin.
	if err := WriteMP4(spec); err != nil {
		return err
	}
	bad := spec
	bad.MediaStartTicks = int64(spec.SampleCount)*int64(spec.SampleDelta) + 90000
	if err := WriteMP4(bad); err != nil {
		return err
	}
	// Manifest keeps the original (valid) window, so the mismatch is the
	// edit list alone and surfaces as an edit-oob error.
	if _, err := WriteManifest(spec); err != nil {
		return err
	}
	return nil
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

// ManifestForNoKeyframe forces every sync flag off, so a random access past
// sample zero has no decodable anchor.
func ManifestForNoKeyframe(m *scan.Manifest) *scan.Manifest {
	cp := *m
	cp.ForceNonSync = make([]uint32, m.SampleCount)
	for i := range cp.ForceNonSync {
		cp.ForceNonSync[i] = uint32(i + 1)
	}
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
