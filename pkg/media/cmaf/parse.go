package cmaf

import (
	"bytes"
	"encoding/binary"
	"os"

	mp4 "github.com/abema/go-mp4"

	"cmafrepair/pkg/media/timebase"
)

// Fragment is the parsed view of one media segment file.
type Fragment struct {
	Name            string // base name
	FileLen         int64
	TrackID         uint32
	Sequence        uint32 // mfhd sequence number
	SeqOffset       int64  // absolute offset of the mfhd payload (for reporting)
	Tfdt            uint64 // declared base media decode time
	TfdtVersion     byte
	TfdtOffset      int64 // absolute offset of the tfdt value field (repair target)
	TfdtValueLen    int   // 4 (v0) or 8 (v1)
	TrunFlags       uint32
	Samples         []Sample
	Moof            BoxHeader
	Mdat            BoxHeader
	MdatOK          bool  // mdat declares exactly the bytes available
	MdatGap         int64 // bytes missing when truncated (0 when intact)
	scanCorruptMdat bool
	scanMdatGap     int64
	Defects         []*timebase.MediaError
}

// InitInfo holds the init segment facts the rest of the pipeline needs.
type InitInfo struct {
	Timescale           uint32 // mdhd timescale (track)
	TrackID             uint32
	TrexDefaultDuration uint32
	TrexDefaultSize     uint32
	TrexDefaultFlags    uint32
	Boxes               []BoxHeader
}

// ParseFragment opens and parses one media fragment. Structural defects are
// returned both in Fragment.Defects and, for hard unreadable cases, as the
// error result. The file is read once, top-level box by top-level box.
func ParseFragment(path string) (*Fragment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, timebase.NewError(timebase.CodeIO, timebase.PhaseScan, "open %s: %v", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, timebase.NewError(timebase.CodeIO, timebase.PhaseScan, "stat %s: %v", path, err)
	}
	frag := &Fragment{Name: baseName(path), FileLen: st.Size()}

	var moofPayload []byte
	var mdatCorrupt bool
	var mdatGap int64
	sc := NewScanner(f, st.Size())
	for sc.Next() {
		h := sc.Header()
		switch h.Type {
		case BoxMoof:
			frag.Moof = h
			b, perr := sc.ReadPayload(0)
			if perr != nil {
				return frag, perr
			}
			moofPayload = b
		case BoxMdat:
			frag.Mdat = h
			if c, gap := sc.Corrupt(); c {
				mdatCorrupt, mdatGap = true, gap
			}
			// Drain only physically present bytes; never read past EOF.
			if serr := sc.Skip(); serr != nil {
				return frag, serr
			}
		default:
			if serr := sc.Skip(); serr != nil {
				return frag, serr
			}
		}
	}
	if err := sc.Err(); err != nil {
		frag.addDefect(timebase.AsMedia(err).WithContext("", frag.Name))
		if len(moofPayload) == 0 {
			return frag, err
		}
	}
	for _, d := range sc.Defects() {
		frag.addDefect(d.WithContext("", frag.Name))
	}
	frag.scanCorruptMdat = mdatCorrupt
	frag.scanMdatGap = mdatGap

	if len(moofPayload) > 0 {
		if err := frag.parseMoof(moofPayload); err != nil {
			return frag, err
		}
	}
	frag.checkMdat()
	return frag, nil
}

// parseMoof decodes mfhd and the traf/tfhd/tfdt/trun chain. All payload
// slices are bounded by the already-validated moof box.
func (f *Fragment) parseMoof(moofPayload []byte) error {
	moofOff := f.Moof.PayloadOff
	children, err := parseContainer(moofPayload, moofOff)
	if err != nil {
		return err
	}
	mfhd := findChild(children, BoxMfhd)
	if mfhd == nil {
		return timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, f.Moof.Offset,
			"moof without mfhd")
	}
	if len(mfhd.payload) < 8 {
		return timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, mfhd.hdr.Offset,
			"mfhd payload too short")
	}
	// Sequence value follows the 4-byte full-box header inside mfhd.
	f.SeqOffset = mfhd.hdr.PayloadOff + 4
	f.Sequence = binary.BigEndian.Uint32(mfhd.payload[4:8])

	traf := findChild(children, BoxTraf)
	if traf == nil {
		return timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, f.Moof.Offset,
			"moof without traf")
	}
	trafChildren, err := parseContainer(traf.payload, traf.hdr.PayloadOff)
	if err != nil {
		return err
	}
	if tfhd := findChild(trafChildren, BoxTfhd); tfhd != nil {
		if id, perr := parseTfhd(tfhd.payload, tfhd.hdr.PayloadOff); perr == nil {
			f.TrackID = id
		} else {
			return perr
		}
	}
	if tfdt := findChild(trafChildren, BoxTfdt); tfdt != nil {
		v, ver, off, vlen, perr := parseTfdt(tfdt.payload, tfdt.hdr.PayloadOff)
		if perr != nil {
			return perr
		}
		f.Tfdt, f.TfdtVersion, f.TfdtOffset, f.TfdtValueLen = v, ver, off, vlen
	}
	if trun := findChild(trafChildren, BoxTrun); trun != nil {
		samples, flags, perr := parseTrun(trun.payload, trun.hdr.PayloadOff)
		if perr != nil {
			return perr
		}
		f.TrunFlags = flags
		f.Samples = samples
	}
	return nil
}

// parseTfhd extracts the track id using go-mp4's flag-aware struct decode.
func parseTfhd(payload []byte, absOff int64) (uint32, error) {
	var tfhd mp4.Tfhd
	if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &tfhd, mp4.Context{}); err != nil {
		return 0, timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, absOff,
			"tfhd unmarshal: %v", err)
	}
	return tfhd.TrackID, nil
}

// parseTfdt returns value, version, absolute offset of the value field and
// its byte length (4 or 8). go-mp4 supplies the version-aware decode.
func parseTfdt(payload []byte, absOff int64) (uint64, byte, int64, int, error) {
	if len(payload) < 5 {
		return 0, 0, 0, 0, timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, absOff,
			"tfdt payload too short")
	}
	ver := payload[0]
	var tfdt mp4.Tfdt
	if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &tfdt, mp4.Context{}); err != nil {
		return 0, 0, 0, 0, timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, absOff,
			"tfdt unmarshal: %v", err)
	}
	// Full-box header occupies payload[0:4]; value starts at payload[4].
	valueOff := absOff + 4
	if ver == 1 {
		return tfdt.GetBaseMediaDecodeTime(), 1, valueOff, 8, nil
	}
	return tfdt.GetBaseMediaDecodeTime(), 0, valueOff, 4, nil
}

// parseTrun decodes the trun box with go-mp4 and converts entries to our
// flat Sample type. Media time/offset assignment happens during repair.
func parseTrun(payload []byte, absOff int64) ([]Sample, uint32, error) {
	var trun mp4.Trun
	if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &trun, mp4.Context{}); err != nil {
		return nil, 0, timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, absOff,
			"trun unmarshal: %v", err)
	}
	out := make([]Sample, 0, trun.SampleCount)
	for i, e := range trun.Entries {
		out = append(out, Sample{
			Index:    uint32(i),
			Duration: e.SampleDuration,
			Size:     e.SampleSize,
			Flags:    e.SampleFlags,
			Sync:     isSyncSample(e.SampleFlags),
		})
	}
	return out, trun.GetFlags(), nil
}

// checkMdat validates the mdat box against actual sample sizes and the
// physical file extent. It records (but does not return) the defect.
func (f *Fragment) checkMdat() {
	if f.Mdat.Type != BoxMdat {
		return
	}
	// The scanner already proved the box length overruns EOF. Replace the
	// generic length defect with the precise truncation defect so one
	// physical fault yields one positionable record.
	if f.scanCorruptMdat {
		f.MdatOK = false
		f.MdatGap = f.scanMdatGap
		f.dropDefect(timebase.CodeBoxLength)
		f.addDefect(timebase.AtError(timebase.CodeMdatTruncated, timebase.PhaseValidate,
			f.FileLen, "mdat truncated: %d declared payload bytes missing",
			f.scanMdatGap).WithBox(BoxMdat).WithContext("", f.Name))
		return
	}
	// Available payload = bytes physically present from payload offset to EOF.
	available := f.FileLen - f.Mdat.PayloadOff
	var declared int64
	if f.Mdat.PayloadLen < 0 {
		declared = available // extends-to-EOF box
	} else {
		declared = f.Mdat.PayloadLen
	}
	// Sum of trun sample sizes is the amount the moof promises.
	var wanted int64
	for i := range f.Samples {
		wanted += int64(f.Samples[i].Size)
	}
	switch {
	case available < declared:
		f.MdatOK = false
		f.MdatGap = declared - available
		f.addDefect(timebase.AtError(timebase.CodeMdatTruncated, timebase.PhaseValidate,
			f.Mdat.PayloadOff+available, "mdat truncated: %d of %d payload bytes missing",
			declared-available, declared).WithBox(BoxMdat))
	case wanted > 0 && wanted > available:
		f.MdatOK = false
		f.MdatGap = wanted - available
		f.addDefect(timebase.AtError(timebase.CodeMdatTruncated, timebase.PhaseValidate,
			f.Mdat.PayloadOff+available, "trun promises %d sample bytes but mdat holds %d",
			wanted, available).WithBox(BoxMdat))
	default:
		f.MdatOK = true
	}
}

func (f *Fragment) addDefect(e *timebase.MediaError) {
	if e != nil {
		f.Defects = append(f.Defects, e)
	}
}

// dropDefect removes all recorded defects of a code (used to collapse a
// generic length error into a precise truncation record).
func (f *Fragment) dropDefect(code timebase.Code) {
	kept := f.Defects[:0]
	for _, d := range f.Defects {
		if d.Code != code {
			kept = append(kept, d)
		}
	}
	f.Defects = kept
}
