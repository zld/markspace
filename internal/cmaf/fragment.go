package cmaf

import (
	"errors"
	"fmt"
)

// ErrZeroDuration means a trun sample declares a zero duration.
var ErrZeroDuration = errors.New("cmaf: zero-duration sample")

// tfhd flag bits (ISO/IEC 14496-12 8.8.12).
const (
	tfhdDefaultBaseIsMoof = 0x020000
	tfhdDefaultSampleDur  = 0x000008
	tfhdDefaultSampleSize = 0x000010
)

// trun flag bits (ISO/IEC 14496-12 8.8.13).
const (
	trunDataOffset      = 0x000001
	trunFirstSampleFlag = 0x000004
	trunSampleDuration  = 0x000100
	trunSampleSize      = 0x000200
	trunSampleFlags     = 0x000400
	trunSampleCTO      = 0x000800
)

// InitInfo describes an fMP4 initialization segment.
type InitInfo struct {
	Timescale uint32 // media timescale from the track mdhd
}

// Sample is one trun entry, measured in the track timescale.
type Sample struct {
	Duration uint32
	Size     uint32
}

// Fragment is the parsed view of a moof/mdat byte sequence.
type Fragment struct {
	Seq         uint32 // mfhd sequence number
	BaseTime    uint64 // tfdt baseMediaDecodeTime
	Timescale   uint32
	Samples     []Sample
	Duration    uint64 // sum of sample durations in timescale units
	PayloadSize uint64 // sum of sample sizes, must fit inside mdat
	Bytes       []byte
	SHA256      [32]byte
}

// ParseInit extracts the track timescale from an initialization segment.
func ParseInit(data []byte) (*InitInfo, error) {
	if len(data) > MaxFragmentSize {
		return nil, fmt.Errorf("%w: init segment too large", ErrTooLarge)
	}
	boxes, err := ScanBoxes(data)
	if err != nil {
		return nil, err
	}
	for _, b := range boxes {
		if b.Type != "moov" {
			continue
		}
		mdhd, perr := FindPath(b.Data, "trak", "mdia", "mdhd")
		if perr != nil {
			return nil, perr
		}
		return parseMDHD(mdhd.Data)
	}
	return nil, errors.New("cmaf: init segment has no moov/trak/mdia/mdhd")
}

func parseMDHD(data []byte) (*InitInfo, error) {
	if len(data) < 4 {
		return nil, ErrTruncated
	}
	version := data[0]
	off := 4
	if version == 1 {
		off += 8 // creation_time
		off += 8 // modification_time
	} else {
		off += 4 + 4
	}
	ts, err := U32(data, off)
	if err != nil {
		return nil, err
	}
	if ts == 0 {
		return nil, errors.New("cmaf: mdhd timescale must not be zero")
	}
	return &InitInfo{Timescale: ts}, nil
}

// ParseFragment parses one moof/mdat fragment against its init timescale.
func ParseFragment(data []byte, timescale uint32) (*Fragment, error) {
	if len(data) > MaxFragmentSize {
		return nil, fmt.Errorf("%w: fragment too large", ErrTooLarge)
	}
	if timescale == 0 {
		return nil, errors.New("cmaf: timescale must not be zero")
	}
	boxes, err := ScanBoxes(data)
	if err != nil {
		return nil, err
	}
	var moof, mdat *Box
	for i := range boxes {
		switch boxes[i].Type {
		case "moof":
			moof = &boxes[i]
		case "mdat":
			mdat = &boxes[i]
		}
	}
	if moof == nil || mdat == nil {
		return nil, errors.New("cmaf: fragment must contain moof and mdat")
	}
	f := &Fragment{Timescale: timescale, Bytes: data}
	if err := parseMoof(moof, f); err != nil {
		return nil, err
	}
	if err := validateMdat(moof, mdat, f); err != nil {
		return nil, err
	}
	return f, nil
}

func parseMoof(moof *Box, f *Fragment) error {
	mfhd, err := FindPath(moof.Data, "mfhd")
	if err != nil {
		return err
	}
	if len(mfhd.Data) < 8 {
		return ErrTruncated
	}
	seq, err := U32(mfhd.Data, 4)
	if err != nil {
		return err
	}
	f.Seq = seq

	traf, err := FindPath(moof.Data, "traf")
	if err != nil {
		return err
	}
	if err := parseTFDT(traf.Data, f); err != nil {
		return err
	}
	defDur, defSize, err := parseTFHD(traf.Data)
	if err != nil {
		return err
	}
	return parseTRUN(traf.Data, f, defDur, defSize)
}

func parseTFDT(trafData []byte, f *Fragment) error {
	tfdt := Find(trafData, "tfdt")
	if tfdt == nil {
		return errors.New("cmaf: traf has no tfdt")
	}
	ver, _, payload, err := tfdt.FullHeader()
	if err != nil {
		return err
	}
	if ver == 1 {
		v, err := U64(payload, 0)
		if err != nil {
			return err
		}
		f.BaseTime = v
		return nil
	}
	v, err := U32(payload, 0)
	if err != nil {
		return err
	}
	f.BaseTime = uint64(v)
	return nil
}

// parseTFHD returns the default sample duration and size (0 when absent).
func parseTFHD(trafData []byte) (uint32, uint32, error) {
	tfhd := Find(trafData, "tfhd")
	if tfhd == nil {
		return 0, 0, errors.New("cmaf: traf has no tfhd")
	}
	ver, flags, payload, err := tfhd.FullHeader()
	if err != nil {
		return 0, 0, err
	}
	_ = ver
	off := 4 // track_ID
	if len(payload) < off {
		return 0, 0, ErrTruncated
	}
	if flags&tfhdDefaultBaseIsMoof == 0 {
		return 0, 0, errors.New("cmaf: tfhd default-base-is-moof flag required")
	}
	var defDur, defSize uint32
	if flags&tfhdDefaultSampleDur != 0 {
		v, err := U32(payload, off)
		if err != nil {
			return 0, 0, err
		}
		defDur, off = v, off+4
	}
	if flags&tfhdDefaultSampleSize != 0 {
		v, err := U32(payload, off)
		if err != nil {
			return 0, 0, err
		}
		defSize, off = v, off+4
	}
	return defDur, defSize, nil
}
