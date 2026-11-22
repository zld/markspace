package index

import (
	"encoding/hex"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/scan"
)

// Builder turns scan output into an on-disk index. It streams records in the
// scanner's bounded batches and writes through a temp file that is renamed
// into place only after the final SHA-256 trailer succeeds, so an
// interrupted build never leaves a half-written cache that could be trusted.
type Builder struct {
	header Header
	dir    string
	final  string
}

// NewBuilder keys the index on the triple required by the spec:
// input byte digest, track configuration and time-base/schema version.
func NewBuilder(cacheDir string, inputDigestHex string, trackID, timescale, sampleCount uint32) (*Builder, error) {
	if sampleCount == 0 {
		return nil, clock.ErrInvalidArgument("index: sample count must be non-zero")
	}
	digest, err := hex.DecodeString(inputDigestHex)
	if err != nil || len(digest) != 32 {
		return nil, clock.ErrInvalidArgument("index: input digest must be 32-byte hex")
	}
	h := Header{
		SchemaVersion: SchemaVersion,
		TrackID:       trackID,
		Timescale:     timescale,
		SampleCount:   sampleCount,
	}
	copy(h.InputDigest[:], digest)
	copy(h.Magic[:], magic)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, clock.ErrRecoverablef("index.cache", "mkdir %s: %v", cacheDir, err)
	}
	final := CachePath(cacheDir, inputDigestHex, trackID)
	return &Builder{header: h, dir: cacheDir, final: final}, nil
}

// Build runs the scanner's bounded batches and emits the complete index.
// Returns the cache status ("created") and the canonical record digest.
func (b *Builder) Build(track *scan.TrackScan, cfg scan.Config) (string, string, error) {
	tmp, err := os.CreateTemp(b.dir, ".idx-*")
	if err != nil {
		return "", "", clock.ErrRecoverablef("index.cache", "create temp: %v", err)
	}
	tmpName := tmp.Name()
	rollback := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	w, err := newStreamWriter(tmp, b.header)
	if err != nil {
		rollback()
		return "", "", err
	}
	var seen uint32
	err = track.ScanBatches(cfg, func(batch []scan.Sample) error {
		for _, s := range batch {
			r := Record{
				Offset:   s.Offset,
				Size:     s.Size,
				WireSize: s.WireSize,
				DTS:      s.DTS,
				DTSNanos: int64(s.DTSNanos),
				CTSNanos: int64(s.CTSNanos),
			}
			if s.Sync {
				r.Flags = flagSync
			}
			if err := w.Append(r); err != nil {
				return err
			}
			seen++
		}
		return nil
	})
	if err != nil {
		rollback()
		return "", "", err
	}
	if seen != b.header.SampleCount {
		rollback()
		return "", "", clock.ErrInvariantf("index.build", "wrote %d records, header says %d", seen, b.header.SampleCount)
	}
	digest, err := w.Finalize()
	if err != nil {
		rollback()
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", "", clock.ErrRecoverablef("index.cache", "close temp: %v", err)
	}
	if err := os.Rename(tmpName, b.final); err != nil {
		os.Remove(tmpName)
		return "", "", clock.ErrRecoverablef("index.cache", "rename: %v", err)
	}
	return "created", digest, nil
}

// CachePath is the deterministic cache location for an (input, track) pair.
func CachePath(cacheDir, inputDigestHex string, trackID uint32) string {
	return filepath.Join(cacheDir, "idx-"+inputDigestHex[:16]+"-t"+itoa(trackID)+".bin")
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
