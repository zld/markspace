package mp4seg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"

	"github.com/abema/go-mp4"

	"gamepack/internal/clock"
	perr "gamepack/internal/errors"
)

// ValidationReport captures the structural checks run on a fragment.
type ValidationReport struct {
	OK            bool   `json:"ok"`
	HasFtyp       bool   `json:"has_ftyp"`
	HasMoov       bool   `json:"has_moov"`
	HasMdat       bool   `json:"has_mdat"`
	Timescale     uint32 `json:"timescale"`
	TrackCount    int    `json:"track_count"`
	SampleCount   int    `json:"sample_count"`
	SHA256        string `json:"sha256"`
	ChecksPassed  int    `json:"checks_passed"`
	ChecksFailed  int    `json:"checks_failed"`
}

// ValidateFragment parses the fragment, confirms the required box hierarchy
// and the shared 90 kHz timescale, and hashes the bytes. A structural
// problem fails with CodeChecksum or CodeStage rather than being tolerated.
func ValidateFragment(r io.ReadSeeker) (*ValidationReport, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, perr.New(subsystem, perr.CodeStage, "read fragment: %v", err)
	}
	sum := sha256.Sum256(raw)
	rep := &ValidationReport{SHA256: hex.EncodeToString(sum[:])}

	seen := map[mp4.BoxType]int{}
	_, err = mp4.ReadBoxStructure(bytes.NewReader(raw), func(handle *mp4.ReadHandle) (interface{}, error) {
		seen[handle.BoxInfo.Type]++
		return nil, nil
	})
	if err != nil {
		rep.ChecksFailed++
		return rep, perr.New(subsystem, perr.CodeStage,
			"fragment box structure unreadable: %v", err).Wrap(err)
	}

	rep.HasFtyp = seen[mp4.BoxTypeFtyp()] > 0
	rep.HasMoov = seen[mp4.BoxTypeMoov()] > 0
	rep.HasMdat = seen[mp4.BoxTypeMdat()] > 0

	for _, ok := range []bool{rep.HasFtyp, rep.HasMoov, rep.HasMdat} {
		if ok {
			rep.ChecksPassed++
		} else {
			rep.ChecksFailed++
		}
	}

	// Probe confirms internal consistency and reports the movie timescale.
	info, err := mp4.Probe(bytes.NewReader(raw))
	if err != nil {
		rep.ChecksFailed++
		return rep, perr.New(subsystem, perr.CodeStage,
			"mp4 probe failed: %v", err).Wrap(err)
	}
	if info.Timescale != uint32(clock.TicksPerSecond) {
		rep.ChecksFailed++
		return rep, perr.New(subsystem, perr.CodeStage,
			"fragment timescale %d != shared %d", info.Timescale, clock.TicksPerSecond)
	}
	rep.Timescale = info.Timescale
	rep.TrackCount = len(info.Tracks)
	if rep.TrackCount < 1 {
		rep.ChecksFailed++
		return rep, perr.New(subsystem, perr.CodeStage, "fragment contains no tracks")
	}
	rep.ChecksPassed++ // track present
	for _, tr := range info.Tracks {
		rep.SampleCount += len(tr.Samples)
	}
	rep.ChecksPassed++ // timescale
	if rep.SampleCount == 0 {
		rep.ChecksFailed++
		return rep, perr.New(subsystem, perr.CodeStage, "fragment contains no samples")
	}
	rep.ChecksPassed++ // samples present
	rep.OK = rep.ChecksFailed == 0
	return rep, nil
}

// Checksum returns the lowercase hex SHA-256 of raw bytes, used for manifest
// integrity and idempotent rerun comparison.
func Checksum(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
