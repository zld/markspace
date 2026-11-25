package pipeline

import (
	"encoding/binary"
	"errors"

	"mediapack/ingest"
	"mediapack/model"
	"mediapack/sample"
)

type mp4Stage struct {
	Data     []byte
	Samples  []ingest.MP4Sample
	Warnings []model.Warning
	Info     *CorruptionInfo
	Err      *model.Error
}

func unitsToMP4Samples(units []*model.Unit) []ingest.MP4Sample {
	out := make([]ingest.MP4Sample, 0, len(units))
	for _, u := range units {
		out = append(out, ingest.MP4Sample{
			Index: u.Index, PTSNanos: u.PTSNanos, DurationNanos: u.DurationNanos,
			Keyframe: u.Keyframe, Payload: u.Payload,
		})
	}
	return out
}

// prepareMP4Stage builds the canonical representation's MP4 fixture, applies
// the requested deterministic corruption, and parses/validates the result.
func prepareMP4Stage(s *sample.Sample, cfg Config) mp4Stage {
	units := s.Video[0]
	clean, err := ingest.BuildFixtureMP4(unitsToMP4Samples(units))
	stage := mp4Stage{Data: clean}
	if err != nil {
		stage.Err = model.NewError(s.Reps[0].Track, "ftyp", 0, model.CategoryOutputWrite, "cannot build MP4 fixture", err)
		return stage
	}
	if _, err := ingest.InspectBoxes(clean); err != nil {
		stage.Err = model.NewError(s.Reps[0].Track, "ftyp", 0, model.CategoryInvalidLength, "go-mp4 cannot inspect generated fixture", err)
		return stage
	}

	data := append([]byte(nil), clean...)
	switch cfg.Corrupt {
	case "truncate":
		target := len(data) * 3 / 4
		cut, cutOffset, boxOffset, err := ingest.TruncateAt(data, target)
		if err != nil {
			stage.Err = model.NewError(s.Reps[0].Track, "fixture", int64(target), model.CategoryInvalidArgument, "cannot apply truncation", err)
			return stage
		}
		data = cut
		stage.Info = &CorruptionInfo{
			Mode: cfg.Corrupt, Offset: cutOffset, BoxOffset: boxOffset,
			Category: model.CategoryTruncatedBox, Recovered: true,
		}

	case "invalid-length":
		parsed, _, _ := ingest.ParseSampleBoxes(s.Reps[0].Track, data)
		if len(parsed) == 0 {
			stage.Err = model.NewError(s.Reps[0].Track, "fixture", -1, model.CategoryInvalidArgument, "no sample box to corrupt", nil)
			return stage
		}
		off := parsed[2].Offset
		binary.BigEndian.PutUint32(data[off:off+4], 4)
		stage.Info = &CorruptionInfo{Mode: cfg.Corrupt, BoxOffset: off, Category: model.CategoryInvalidLength}

	case "missing-keyframe":
		parsed, _, _ := ingest.ParseSampleBoxes(s.Reps[0].Track, data)
		var off int64 = -1
		for _, smp := range parsed {
			if smp.PTSNanos == 6_000_000_000 && smp.Keyframe {
				off = smp.Offset
				break
			}
		}
		if off < 0 {
			stage.Err = model.NewError(s.Reps[0].Track, "fixture", -1, model.CategoryMissingKeyframe, "keyframe to remove was not generated", nil)
			return stage
		}
		data[off+9] &^= 0x01
		stage.Info = &CorruptionInfo{Mode: cfg.Corrupt, BoxOffset: off, Category: model.CategoryMissingKeyframe, Recovered: true}

	case "crc":
		parsed, _, _ := ingest.ParseSampleBoxes(s.Reps[0].Track, data)
		if len(parsed) < 3 {
			stage.Err = model.NewError(s.Reps[0].Track, "fixture", -1, model.CategoryInvalidArgument, "not enough samples to corrupt CRC", nil)
			return stage
		}
		off := parsed[2].Offset
		data[off+32] ^= 0xFF
		stage.Info = &CorruptionInfo{Mode: cfg.Corrupt, BoxOffset: off, Category: model.CategoryIntegrityMismatch, Recovered: true}
	}

	stage.Data = data
	parsed, warnings, perr := ingest.ParseSampleBoxes(s.Reps[0].Track, data)
	stage.Samples = parsed
	stage.Warnings = warnings
	if perr != nil {
		var me *model.Error
		if errors.As(perr, &me) {
			stage.Err = me
			if stage.Info != nil {
				stage.Info.Category = me.Category
				stage.Info.Message = me.Message
				stage.Info.Recovered = me.Recoverable
			}
		} else {
			stage.Err = model.NewError(s.Reps[0].Track, "mp4", -1, model.CategoryInvalidLength, "unexpected MP4 parse error", perr)
		}
	}
	return stage
}
