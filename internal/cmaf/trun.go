package cmaf

import (
	"errors"
	"fmt"
)

// parseTRUN reads the sample table, rejecting truncated tables, zero-duration
// samples and overflowing duration/size accumulators.
func parseTRUN(trafData []byte, f *Fragment, defDur, defSize uint32) error {
	trun := Find(trafData, "trun")
	if trun == nil {
		return errors.New("cmaf: traf has no trun")
	}
	ver, flags, payload, err := trun.FullHeader()
	if err != nil {
		return err
	}
	_ = ver
	count, err := U32(payload, 0)
	if err != nil {
		return err
	}
	off := 4
	if flags&trunDataOffset != 0 {
		off += 4 // data_offset is validated separately against mdat
	}
	if flags&trunFirstSampleFlag != 0 {
		off += 4
	}
	if count == 0 {
		return errors.New("cmaf: trun contains no samples")
	}
	entrySize := trunEntrySize(flags)
	// Guard the multiplication before touching the sample table.
	if off > len(payload) || entrySize == 0 ||
		uint64(count) > uint64(len(payload)-off)/uint64(entrySize) {
		return fmt.Errorf("%w: trun declares %d samples in %d bytes",
			ErrTruncated, count, len(payload)-off)
	}
	samples := make([]Sample, count)
	var totalDur, totalSize uint64
	for i := range samples {
		base := off + i*entrySize
		var dur, size uint32
		if flags&trunSampleDuration != 0 {
			v, err := U32(payload, base)
			if err != nil {
				return err
			}
			dur = v
		} else {
			dur = defDur
		}
		if flags&trunSampleSize != 0 {
			pos := base
			if flags&trunSampleDuration != 0 {
				pos += 4
			}
			v, err := U32(payload, pos)
			if err != nil {
				return err
			}
			size = v
		} else {
			size = defSize
		}
		if dur == 0 {
			return fmt.Errorf("%w: sample %d of fragment %d",
				ErrZeroDuration, i, f.Seq)
		}
		if size == 0 {
			return fmt.Errorf("cmaf: sample %d of fragment %d has zero size", i, f.Seq)
		}
		totalDur += uint64(dur)
		totalSize += uint64(size)
		if totalDur < uint64(dur) || totalSize < uint64(size) {
			return ErrOverflow
		}
		samples[i] = Sample{Duration: dur, Size: size}
	}
	f.Samples = samples
	f.Duration = totalDur
	f.PayloadSize = totalSize
	return nil
}

func trunEntrySize(flags uint32) int {
	n := 0
	if flags&trunSampleDuration != 0 {
		n++
	}
	if flags&trunSampleSize != 0 {
		n++
	}
	if flags&trunSampleFlags != 0 {
		n++
	}
	if flags&trunSampleCTO != 0 {
		n++
	}
	return 4 * n
}

// validateMdat ensures the declared sample payload fits inside the mdat box.
func validateMdat(moof, mdat *Box, f *Fragment) error {
	if uint64(len(mdat.Data)) < f.PayloadSize {
		return fmt.Errorf("%w: samples need %d bytes, mdat holds %d",
			ErrTruncated, f.PayloadSize, len(mdat.Data))
	}
	if uint64(moof.Start) > uint64(1<<63)-uint64(moof.Size) {
		return ErrOverflow
	}
	return nil
}
