// Package mp4scan parses MP4 containers through go-mp4 and preserves the
// container/edit/zero timeline distinction.
package mp4scan

import (
	"os"

	"github.com/abema/go-mp4"

	"archivecheck/internal/clock"
	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
)

// ScanAsset opens and parses the MP4 referenced by asset.
func ScanAsset(asset model.InputAsset) (*ScanResult, error) {
	sum, err := hashFile(asset.Path)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindIO, err).WithPath(asset.Path).WithStage("scan").WithAsset(asset.ID)
	}
	f, err := os.Open(asset.Path)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindIO, err).WithPath(asset.Path).WithStage("scan").WithAsset(asset.ID)
	}
	defer f.Close()

	paths := []mp4.BoxPath{
		boxPath("moov", "mvhd"),
		boxPath("moov", "trak", "tkhd"),
		boxPath("moov", "trak", "mdia", "mdhd"),
		boxPath("moov", "trak", "edts", "elst"),
		boxPath("moov", "trak", "mdia", "minf", "stbl", "stts"),
		boxPath("moov", "trak", "mdia", "minf", "stbl", "ctts"),
		boxPath("moov", "trak", "mdia", "minf", "stbl", "stss"),
		boxPath("moov", "trak", "mdia", "minf", "stbl", "stsz"),
		boxPath("moov", "trak", "mdia", "minf", "stbl", "stco"),
	}
	boxes, err := mp4.ExtractBoxesWithPayload(f, nil, paths)
	if err != nil {
		return nil, classifyParseError(err).WithPath(asset.Path).WithStage("scan").WithAsset(asset.ID)
	}

	r := &ScanResult{Path: asset.Path, SHA256: sum, TrackID: asset.TrackID}
	movieTimescale := uint32(clock.TicksPerSecond)
	var mdhd *mp4.Mdhd
	var elst *mp4.Elst
	var stts *mp4.Stts
	var ctts *mp4.Ctts
	var stss *mp4.Stss
	var stsz *mp4.Stsz
	for _, b := range boxes {
		switch p := b.Payload.(type) {
		case *mp4.Mvhd:
			if p.Timescale != 0 {
				movieTimescale = p.Timescale
			}
		case *mp4.Tkhd:
			if asset.TrackID != 0 && p.TrackID != asset.TrackID {
				return nil, apperrors.E(apperrors.KindContainer, "track id mismatch").
					WithStage("scan").WithAsset(asset.ID).WithPath(asset.Path)
			}
			r.TrackID = p.TrackID
		case *mp4.Mdhd:
			mdhd = p
		case *mp4.Elst:
			elst = p
		case *mp4.Stts:
			stts = p
		case *mp4.Ctts:
			ctts = p
		case *mp4.Stss:
			stss = p
		case *mp4.Stsz:
			stsz = p
		}
	}
	if mdhd == nil {
		return nil, apperrors.E(apperrors.KindContainer, "missing mdhd time base").
			WithStage("scan").WithAsset(asset.ID).WithPath(asset.Path)
	}
	tb, err := clock.NewTimebase(mdhd.Timescale)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.KindContainer, err).
			WithStage("scan").WithAsset(asset.ID).WithPath(asset.Path)
	}
	r.Timebase = tb

	if se := buildSamples(r, tb, stts, ctts, stss, stsz); se != nil {
		return nil, se.WithStage("scan").WithAsset(asset.ID).WithPath(asset.Path)
	}
	if ee := buildEdits(r, tb, clock.Timebase{Timescale: movieTimescale}, mdhd, elst, asset.EditIntervals); ee != nil {
		return nil, ee.WithStage("scan").WithAsset(asset.ID).WithPath(asset.Path)
	}
	buildTimeline(r)
	r.Timeline.AssetID = asset.ID
	if err := r.Timeline.ValidateZeroPreservation(); err != nil {
		return nil, apperrors.Wrap(apperrors.KindContainer, err).
			WithStage("scan").WithAsset(asset.ID).WithPath(asset.Path)
	}
	if asset.StartTimestampTicks != 0 &&
		clock.Abs(clock.Tick(asset.StartTimestampTicks), r.Timeline.ContainerStart) > 1 {
		return nil, apperrors.E(apperrors.KindManifest,
			"manifest start_timestamp_ticks does not match parsed container PTS origin").
			WithStage("scan").WithAsset(asset.ID).WithPath(asset.Path)
	}
	if ke := validateKeyframes(r); ke != nil {
		return nil, ke.WithStage("scan").WithAsset(asset.ID).WithPath(asset.Path)
	}
	return r, nil
}
