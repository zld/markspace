package cmaf

import (
	"bytes"
	"encoding/binary"
	"os"

	mp4 "github.com/abema/go-mp4"

	"cmafrepair/pkg/media/timebase"
)

// ParseInit reads the init segment and extracts track timescale plus trex
// defaults via a bounded top-level scan and go-mp4 structure decoding.
func ParseInit(path string) (*InitInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, timebase.NewError(timebase.CodeIO, timebase.PhaseScan, "open %s: %v", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, timebase.NewError(timebase.CodeIO, timebase.PhaseScan, "stat %s: %v", path, err)
	}
	info := &InitInfo{}
	err = ScanTopLevel(f, st.Size(), func(s *Scanner, h BoxHeader) error {
		info.Boxes = append(info.Boxes, h)
		if h.Type != BoxMoov {
			return s.Skip()
		}
		payload, perr := s.ReadPayload(0)
		if perr != nil {
			return perr
		}
		return info.parseMoov(payload, h.PayloadOff)
	})
	if err != nil {
		return nil, err
	}
	if info.Timescale == 0 {
		return nil, timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseScan,
			"init segment has no track timescale")
	}
	return info, nil
}

func (i *InitInfo) parseMoov(payload []byte, absOff int64) error {
	children, err := parseContainer(payload, absOff)
	if err != nil {
		return err
	}
	for _, c := range children {
		switch c.hdr.Type {
		case "trak":
			if err := i.parseTrak(c.payload, c.hdr.PayloadOff); err != nil {
				return err
			}
		case "mvex":
			if err := i.parseMvex(c.payload, c.hdr.PayloadOff); err != nil {
				return err
			}
		}
	}
	return nil
}

func (i *InitInfo) parseTrak(payload []byte, absOff int64) error {
	children, err := parseContainer(payload, absOff)
	if err != nil {
		return err
	}
	mdia := findChild(children, "mdia")
	if mdia == nil {
		return nil
	}
	mdiaKids, err := parseContainer(mdia.payload, mdia.hdr.PayloadOff)
	if err != nil {
		return err
	}
	mdhd := findChild(mdiaKids, "mdhd")
	if mdhd == nil {
		return nil
	}
	var mdhdBox mp4.Mdhd
	if _, err := mp4.Unmarshal(bytes.NewReader(mdhd.payload), uint64(len(mdhd.payload)), &mdhdBox, mp4.Context{}); err != nil {
		return timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, mdhd.hdr.Offset,
			"mdhd unmarshal: %v", err)
	}
	i.Timescale = mdhdBox.Timescale

	tkhd := findChild(children, "tkhd")
	if tkhd != nil && len(tkhd.payload) >= 8+4+4+4+4 {
		// tkhd: version/flags(4) creation(4/8) modification(4/8) trackID(4)
		// We only need track id for the common version-0 layout.
		if tkhd.payload[0] == 0 {
			i.TrackID = binary.BigEndian.Uint32(tkhd.payload[12:16])
		}
	}
	return nil
}

func (i *InitInfo) parseMvex(payload []byte, absOff int64) error {
	children, err := parseContainer(payload, absOff)
	if err != nil {
		return err
	}
	trex := findChild(children, "trex")
	if trex == nil {
		return nil
	}
	var t mp4.Trex
	if _, err := mp4.Unmarshal(bytes.NewReader(trex.payload), uint64(len(trex.payload)), &t, mp4.Context{}); err != nil {
		return timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, trex.hdr.Offset,
			"trex unmarshal: %v", err)
	}
	if i.TrackID == 0 {
		i.TrackID = t.TrackID
	}
	i.TrexDefaultDuration = t.DefaultSampleDuration
	i.TrexDefaultSize = t.DefaultSampleSize
	i.TrexDefaultFlags = t.DefaultSampleFlags
	return nil
}

// baseName returns the final path component without importing filepath in the
// hot parse path.
func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == os.PathSeparator {
			return p[i+1:]
		}
	}
	return p
}

// FragmentName returns the base file name of a fragment path and is exported
// for pipeline error context.
func FragmentName(p string) string { return baseName(p) }
