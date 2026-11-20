package index

import (
	"fmt"

	"mp4seek/internal/bmff"
)

// buildTrun expands one trun box. dts is the decode time of the first
// sample in the run; dataOff is the file offset of the run's media data.
// Returns the decode time after the last sample and the next data offset.
func buildTrun(r *bmff.Reader, trun bmff.Box, tfhdFlags, defDur, defSize, defFlags uint32,
	trex TrexDefaults, idx *FragIndex, dts, dataOff int64) (endDTS, nextOff int64, err error) {

	data, err := r.ReadBoxData(trun, bmff.MaxMetaBox)
	if err != nil {
		return 0, 0, err
	}
	c := &cursor{data: data, path: trun.Path}
	version, flags, err := c.fullBoxHeader()
	if err != nil {
		return 0, 0, err
	}
	if version > 1 {
		return 0, 0, c.fail(fmt.Sprintf("trun version %d unsupported", version))
	}
	count, err := c.u32()
	if err != nil {
		return 0, 0, err
	}
	if count > MaxSamples {
		return 0, 0, c.fail(fmt.Sprintf("trun sample count %d too large", count))
	}
	var dataOffset int64
	if flags&trunDataOffset != 0 {
		v, err := c.i32()
		if err != nil {
			return 0, 0, err
		}
		dataOffset = int64(v)
	}
	var firstFlags uint32
	haveFirst := false
	if flags&trunFirstSampleFlags != 0 {
		if firstFlags, err = c.u32(); err != nil {
			return 0, 0, err
		}
		haveFirst = true
	}

	// Effective per-field defaults: trun row > tfhd default > trex.
	dur := func(v uint32, present bool) (uint32, error) {
		if present {
			return v, nil
		}
		if tfhdFlags&tfhdDefaultDuration != 0 {
			return defDur, nil
		}
		if trex.Present {
			return trex.Duration, nil
		}
		return 0, c.fail("no sample duration source")
	}
	off := dataOff + dataOffset
	cur := dts
	for i := uint32(0); i < count; i++ {
		var d, sz, fl uint32
		var cto int64
		var v uint32
		present := flags&trunSampleDuration != 0
		if present {
			if v, err = c.u32(); err != nil {
				return 0, 0, err
			}
		}
		if d, err = dur(v, present); err != nil {
			return 0, 0, err
		}
		if flags&trunSampleSize != 0 {
			if sz, err = c.u32(); err != nil {
				return 0, 0, err
			}
		} else if tfhdFlags&tfhdDefaultSize != 0 {
			sz = defSize
		} else if trex.Present {
			sz = trex.Size
		} else {
			return 0, 0, c.fail("no sample size source")
		}
		switch {
		case flags&trunSampleFlags != 0:
			if fl, err = c.u32(); err != nil {
				return 0, 0, err
			}
		case haveFirst && i == 0:
			fl = firstFlags
		case tfhdFlags&tfhdDefaultFlags != 0:
			fl = defFlags
		case trex.Present:
			fl = trex.Flags
		}
		if flags&trunSampleCTO != 0 {
			if version == 1 {
				v32, err := c.i32()
				if err != nil {
					return 0, 0, err
				}
				cto = int64(v32)
			} else {
				v32, err := c.u32()
				if err != nil {
					return 0, 0, err
				}
				cto = int64(v32)
			}
		}
		if sz > bmff.MaxSample {
			return 0, 0, c.fail(fmt.Sprintf("sample size %d exceeds cap", sz))
		}
		if off < 0 || off > r.Size()-int64(sz) {
			return 0, 0, fmt.Errorf("%w: %s: sample %d range [%d,%d) outside file of %d bytes",
				bmff.ErrCorrupt, trun.Path, len(idx.Samples), off, off+int64(sz), r.Size())
		}
		s := FragSample{
			Index:  len(idx.Samples),
			Offset: off,
			Size:   sz,
			DTS:    cur,
			CTO:    cto,
			PTS:    cur + cto,
			Key:    fl&flagNonSyncSample == 0,
		}
		idx.Samples = append(idx.Samples, s)
		if s.DTS > idx.MaxDTS {
			idx.MaxDTS = s.DTS
		}
		cur += int64(d)
		off += int64(sz)
	}
	return cur, off, nil
}
