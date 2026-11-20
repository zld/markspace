package index

import (
	"fmt"

	"mp4seek/internal/bmff"
)

// FragSample is one sample expanded from moof/traf run data.
type FragSample struct {
	Index  int
	Offset int64
	Size   uint32
	DTS    int64
	CTO    int64
	PTS    int64
	Key    bool
}

// FragIndex is the expanded view of one track across movie fragments.
type FragIndex struct {
	Samples    []FragSample
	WrapEvents int   // tfdt version-0 32-bit wraparounds unfolded
	MaxDTS     int64 // largest sample DTS after unfolding
}

// TrexDefaults carries mvex/trex default sample fields for a track.
type TrexDefaults struct {
	Duration uint32
	Size     uint32
	Flags    uint32
	Present  bool
}

// ParseTrex finds the trex defaults for trackID under moov/mvex.
func ParseTrex(r *bmff.Reader, moov bmff.Box, trackID uint32) (TrexDefaults, error) {
	var out TrexDefaults
	kids, err := r.Children(moov)
	if err != nil {
		return out, err
	}
	for _, k := range kids {
		if k.Type != "mvex" {
			continue
		}
		grand, err := r.Children(k)
		if err != nil {
			return out, err
		}
		for _, g := range grand {
			if g.Type != "trex" {
				continue
			}
			data, err := r.ReadBoxData(g, bmff.MaxMetaBox)
			if err != nil {
				return out, err
			}
			c := &cursor{data: data, path: g.Path}
			if _, _, err := c.fullBoxHeader(); err != nil {
				return out, err
			}
			id, err := c.u32()
			if err != nil {
				return out, err
			}
			if _, err := c.u32(); err != nil { // description index
				return out, err
			}
			d, err := c.u32()
			if err != nil {
				return out, err
			}
			s, err := c.u32()
			if err != nil {
				return out, err
			}
			f, err := c.u32()
			if err != nil {
				return out, err
			}
			if id == trackID {
				out = TrexDefaults{Duration: d, Size: s, Flags: f, Present: true}
			}
		}
	}
	return out, nil
}

const (
	tfhdBaseDataOffset    = 0x000001
	tfhdDefaultDuration   = 0x000008
	tfhdDefaultSize       = 0x000010
	tfhdDefaultFlags      = 0x000020
	tfhdDefaultBaseIsMoof = 0x020000
	trunDataOffset        = 0x000001
	trunFirstSampleFlags  = 0x000004
	trunSampleDuration    = 0x000100
	trunSampleSize        = 0x000200
	trunSampleFlags       = 0x000400
	trunSampleCTO         = 0x800
	flagNonSyncSample     = 0x00010000
	wrapThreshold         = 1 << 31 // raw tfdt backward jump implying a wrap
)

// BuildFragmented expands all moof boxes (in file order) for trackID.
// tfdt version 0 values are unfolded onto a 64-bit timeline: a backward
// jump of more than half the 32-bit range is treated as one wrap event;
// any other regression is an error.
func BuildFragmented(r *bmff.Reader, moofs []bmff.Box, trackID uint32, trex TrexDefaults) (*FragIndex, error) {
	idx := &FragIndex{}
	var wraps uint64
	var prevRaw uint32
	var prevEnd int64
	havePrev := false

	for _, moof := range moofs {
		kids, err := r.Children(moof)
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			if k.Type != "traf" {
				continue
			}
			end, err := buildTraf(r, k, moof, trackID, trex, idx, &wraps, &prevRaw, &prevEnd, &havePrev)
			if err != nil {
				return nil, err
			}
			_ = end
		}
	}
	return idx, nil
}

func buildTraf(r *bmff.Reader, traf, moof bmff.Box, trackID uint32, trex TrexDefaults,
	idx *FragIndex, wraps *uint64, prevRaw *uint32, prevEnd *int64, havePrev *bool) (int64, error) {

	boxes, err := r.Children(traf)
	if err != nil {
		return 0, err
	}
	// tfhd
	var tfhdFlags, defDur, defSize, defFlags uint32
	var baseOffset int64
	haveBase := false
	thisTrack := false
	var tfdt uint64
	tfdtV0 := false
	haveTfdt := false
	var truns []bmff.Box

	for _, b := range boxes {
		data, err := r.ReadBoxData(b, bmff.MaxMetaBox)
		if err != nil {
			return 0, err
		}
		c := &cursor{data: data, path: b.Path}
		switch b.Type {
		case "tfhd":
			var flags uint32
			if _, flags, err = c.fullBoxHeader(); err != nil {
				return 0, err
			}
			id, err := c.u32()
			if err != nil {
				return 0, err
			}
			if id != trackID {
				return 0, nil // other track's traf
			}
			thisTrack = true
			tfhdFlags = flags
			if flags&tfhdBaseDataOffset != 0 {
				v, err := c.u64()
				if err != nil {
					return 0, err
				}
				if v > uint64(1<<62) {
					return 0, c.fail("tfhd base-data-offset overflows")
				}
				baseOffset = int64(v)
				haveBase = true
			}
			if flags&0x000002 != 0 { // sample-description-index
				if _, err := c.u32(); err != nil {
					return 0, err
				}
			}
			if flags&tfhdDefaultDuration != 0 {
				if defDur, err = c.u32(); err != nil {
					return 0, err
				}
			}
			if flags&tfhdDefaultSize != 0 {
				if defSize, err = c.u32(); err != nil {
					return 0, err
				}
			}
			if flags&tfhdDefaultFlags != 0 {
				if defFlags, err = c.u32(); err != nil {
					return 0, err
				}
			}
		case "tfdt":
			version, _, err := c.fullBoxHeader()
			if err != nil {
				return 0, err
			}
			if version == 1 {
				if tfdt, err = c.u64(); err != nil {
					return 0, err
				}
			} else if version == 0 {
				v, err := c.u32()
				if err != nil {
					return 0, err
				}
				tfdt = uint64(v)
				tfdtV0 = true
			} else {
				return 0, c.fail(fmt.Sprintf("tfdt version %d unsupported", version))
			}
			haveTfdt = true
		case "trun":
			truns = append(truns, b)
		}
	}
	if !thisTrack {
		return 0, nil
	}
	if !haveBase {
		if tfhdFlags&tfhdDefaultBaseIsMoof != 0 {
			baseOffset = moof.Offset
			haveBase = true
		} else {
			return 0, fmt.Errorf("%w: %s: no base-data-offset and default-base-is-moof unset",
				bmff.ErrCorrupt, traf.Path)
		}
	}

	// Establish the decode-time base, unfolding tfdt v0 wraps.
	var base int64
	if haveTfdt {
		if tfdtV0 {
			raw := uint32(tfdt)
			base = int64(raw) + int64(*wraps<<32)
			if *havePrev && base < *prevEnd {
				if *prevRaw-raw > wrapThreshold {
					*wraps++
					idx.WrapEvents++
					base = int64(raw) + int64(*wraps<<32)
					if base < *prevEnd {
						return 0, fmt.Errorf("%w: %s: tfdt %d regresses below %d even after wrap",
							bmff.ErrCorrupt, traf.Path, raw, *prevEnd)
					}
				} else {
					return 0, fmt.Errorf("%w: %s: tfdt %d regresses below previous end %d",
						bmff.ErrCorrupt, traf.Path, raw, *prevEnd)
				}
			}
			*prevRaw = raw
		} else {
			base = int64(tfdt)
			if *havePrev && base < *prevEnd {
				return 0, fmt.Errorf("%w: %s: 64-bit tfdt %d regresses below previous end %d",
					bmff.ErrCorrupt, traf.Path, tfdt, *prevEnd)
			}
		}
	} else if *havePrev {
		base = *prevEnd
	}

	dts := base
	dataOff := baseOffset
	for _, trun := range truns {
		end, next, err := buildTrun(r, trun, tfhdFlags, defDur, defSize, defFlags, trex,
			idx, dts, dataOff)
		if err != nil {
			return 0, err
		}
		dts = end
		dataOff = next
	}
	if !*havePrev || dts > *prevEnd {
		*prevEnd = dts
	}
	*havePrev = true
	return dts, nil
}
