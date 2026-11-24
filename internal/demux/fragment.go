package demux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// trexDefaults holds per-track defaults from the movie extends box.
type trexDefaults struct {
	descriptionIndex uint32
	duration         uint32
	size             uint32
	flags            uint32
}

func parseTrex(moov *rawBox) map[uint32]trexDefaults {
	out := make(map[uint32]trexDefaults)
	if moov == nil {
		return out
	}
	mvex := moov.find("mvex")
	if mvex == nil {
		return out
	}
	for _, trex := range mvex.findAll("trex") {
		r := newBinReader(trex, fault.OpBoxHeader)
		if _, _, err := r.fullBox(); err != nil {
			continue
		}
		id, err := r.u32()
		if err != nil {
			continue
		}
		d := trexDefaults{}
		if d.descriptionIndex, err = r.u32(); err != nil {
			continue
		}
		if d.duration, err = r.u32(); err != nil {
			continue
		}
		if d.size, err = r.u32(); err != nil {
			continue
		}
		if d.flags, err = r.u32(); err != nil {
			continue
		}
		out[id] = d
	}
	return out
}

// fragmentLayout is the parse result of one moof/mdat pair.
type fragmentLayout struct {
	moofOffset int64
	dataOffset int64
	size       int64
	trackIDs   []uint32
	samples    map[uint32][]Sample
	payload    []byte
}

// parseFragment interprets one moof.  dataStart is the absolute offset of the
// following mdat payload.  All offsets are absolute positions in the input.
func parseFragment(moof *rawBox, dataStart int64, end int64, trex map[uint32]trexDefaults) (*fragmentLayout, error) {
	layout := &fragmentLayout{moofOffset: moof.off, dataOffset: dataStart, samples: make(map[uint32][]Sample)}
	base := moof.off
	if mfhd := moof.find("mfhd"); mfhd != nil {
		_ = mfhd
	}
	for _, traf := range moof.findAll("traf") {
		tfhd := traf.find("tfhd")
		if tfhd == nil {
			return nil, fault.New(fault.StageDemux, "mp4", fault.OpBoxHeader, traf.payloadOff(), "traf without tfhd")
		}
		r := newBinReader(tfhd, fault.OpBoxHeader)
		_, flags, err := r.fullBox()
		if err != nil {
			return nil, err
		}
		trackID, err := r.u32()
		if err != nil {
			return nil, err
		}
		tdef := trex[trackID]
		var baseOffset int64
		if flags&0x000001 != 0 {
			v, err := r.u64()
			if err != nil {
				return nil, err
			}
			baseOffset = int64(v)
		} else if flags&0x020000 != 0 {
			baseOffset = base
		}
		if flags&0x000002 != 0 {
			if _, err := r.u32(); err != nil {
				return nil, err
			}
		}
		defDuration := tdef.duration
		if flags&0x000008 != 0 {
			if defDuration, err = r.u32(); err != nil {
				return nil, err
			}
		}
		defSize := tdef.size
		if flags&0x000010 != 0 {
			if defSize, err = r.u32(); err != nil {
				return nil, err
			}
		}
		defFlags := tdef.flags
		if flags&0x000020 != 0 {
			if defFlags, err = r.u32(); err != nil {
				return nil, err
			}
		}
		_ = defFlags

		var baseTime uint64
		if tfdt := traf.find("tfdt"); tfdt != nil {
			fr := newBinReader(tfdt, fault.OpBoxHeader)
			version, _, err := fr.fullBox()
			if err != nil {
				return nil, err
			}
			if version == 1 {
				if baseTime, err = fr.u64(); err != nil {
					return nil, err
				}
			} else {
				v, err := fr.u32()
				if err != nil {
					return nil, err
				}
				baseTime = uint64(v)
			}
		}

		trunDataStart := dataStart
		if baseOffset != 0 {
			trunDataStart = baseOffset
		}
		layout.trackIDs = appendUnique(layout.trackIDs, trackID)
		for _, trun := range traf.findAll("trun") {
			rr := newBinReader(trun, fault.OpBoxHeader)
			_, trunFlags, err := rr.fullBox()
			if err != nil {
				return nil, err
			}
			count, err := rr.u32()
			if err != nil {
				return nil, err
			}
			dataOffset := trunDataStart
			if trunFlags&0x000001 != 0 {
				v, err := rr.u32()
				if err != nil {
					return nil, err
				}
				dataOffset = int64(int32(v)) + base
			} else if baseOffset != 0 {
				dataOffset = trunDataStart
			}
			if trunFlags&0x000004 != 0 {
				if _, err := rr.u32(); err != nil {
					return nil, err
				}
			}
			dts := int64(baseTime)
			pos := dataOffset
			runEnd := dataOffset
			for i := uint32(0); i < count; i++ {
				duration := defDuration
				if trunFlags&0x000100 != 0 {
					if duration, err = rr.u32(); err != nil {
						return nil, err
					}
				}
				size := defSize
				if trunFlags&0x000200 != 0 {
					if size, err = rr.u32(); err != nil {
						return nil, err
					}
				}
				if trunFlags&0x000400 != 0 {
					if _, err := rr.u32(); err != nil {
						return nil, err
					}
				}
				var ctsOff int64
				if trunFlags&0x000800 != 0 {
					raw, err := rr.u32()
					if err != nil {
						return nil, err
					}
					ctsOff = int64(int32(raw))
				}
				if pos+int64(size) > end {
					return nil, fault.New(fault.StageDemux, "mp4", fault.OpTruncated, end,
						"fragment sample extends beyond input")
				}
				layout.samples[trackID] = append(layout.samples[trackID], Sample{
					Offset: pos,
					Size:   size,
					Delta:  duration,
					DTS:    clock.New(dts, 0),
					CTS:    clock.New(dts+ctsOff, 0),
				})
				pos += int64(size)
				dts += int64(duration)
				runEnd = pos
			}
			if runEnd > layout.dataOffset+layout.size {
				layout.size = runEnd - layout.dataOffset
			}
			layout.trackIDs = appendUnique(layout.trackIDs, trackID)
		}
	}
	return layout, nil
}

func appendUnique(ids []uint32, id uint32) []uint32 {
	for _, v := range ids {
		if v == id {
			return ids
		}
	}
	return append(ids, id)
}
