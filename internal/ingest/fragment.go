package ingest

import (
	"fmt"

	"github.com/abema/go-mp4"

	"mediacore/internal/fault"
)

// trun flag bits defined by ISO/IEC 14496-12 8.8.8.1.
const (
	trunDataOffsetPresent       = 0x000001
	trunFirstSampleFlagsPresent = 0x000004
	trunSampleDurationPresent   = 0x000100
	trunSampleSizePresent       = 0x000200
	trunSampleFlagsPresent      = 0x000400
	trunCtsOffsetPresent        = 0x000800
)

// Sample is one sample located by a fragment, with exact byte ranges.
type Sample struct {
	Index    int   `json:"index"`
	Offset   int64 `json:"offset"`
	Size     int64 `json:"size"`
	Duration int64 `json:"duration"`
	Cts      int64 `json:"cts_offset"`
	// DeclOffset is the raw offset of the trun field that declared Size. It
	// is the anchor reported when the declared range is impossible.
	DeclOffset int64 `json:"decl_offset"`
}

// Fragment is one moof+mdat pair with the fields needed to rebuild a timeline.
type Fragment struct {
	Index       int      `json:"index"`
	File        string   `json:"file"`
	TrackID     uint32   `json:"track_id"`
	Sequence    uint32   `json:"sequence"`
	MoofOffset  int64    `json:"moof_offset"`
	MoofSize    int64    `json:"moof_size"`
	MdatOffset  int64    `json:"mdat_offset"`
	MdatSize    int64    `json:"mdat_size"`
	TfdtOffset  int64    `json:"tfdt_offset"`
	Tfdt        uint64   `json:"tfdt"`
	TfdtVersion uint8    `json:"tfdt_version"`
	TrunOffset  int64    `json:"trun_offset"`
	Samples     []Sample `json:"samples"`
}

// End returns the exclusive end of the mdat.
func (f Fragment) End() int64 { return f.MdatOffset + f.MdatSize }

// ParseFragments scans the fragment region after the initialization segment.
// A partially written index is reported but does not stop the scan: the moof
// headers are the authority.
func ParseFragments(r *Reader, start int64) ([]Fragment, []*fault.Error) {
	var (
		frags []Fragment
		errs  []*fault.Error
	)
	for off := start; off < r.Len(); {
		h, ok, f := r.Next(off)
		if !ok {
			if f != nil {
				errs = append(errs, f)
			}
			break
		}
		switch h.Type {
		case "moof":
			frag, more, f := r.parseMoof(h, len(frags))
			if f != nil {
				errs = append(errs, f)
				// Recovery: continue after the damaged moof only if its size
				// was trustworthy. A truncated/oversize box ends the scan.
				if f.Code != fault.CodeBadPayload && f.Code != fault.CodeMissingField {
					return frags, errs
				}
				off = h.End()
				continue
			}
			errs = append(errs, more...)
			frags = append(frags, *frag)
			off = frag.End()
		case "sidx":
			off = h.End()
		case "styp":
			off = h.End()
		case "emsg":
			off = h.End()
		case "mdat":
			// An mdat at top level without a preceding moof is not part of
			// the supported layout; skip by declared size only.
			off = h.End()
		default:
			errs = append(errs, fault.New(fault.CodeBadPayload, r.File, h.Offset, h.Type, "unexpected top-level box"))
			off = h.End()
		}
	}
	return frags, errs
}

func (r *Reader) parseMoof(moof Header, index int) (*Fragment, []*fault.Error, *fault.Error) {
	frag := &Fragment{Index: index, File: r.File, MoofOffset: moof.Offset, MoofSize: moof.Size}
	var mfhdSeen, trafSeen, tfdtSeen, tfhdSeen bool
	var trafOffset int64 = -1
	// dataBase is the base data offset used by trun entries. With
	// default-base-is-moof it is the first byte of the moof box; an explicit
	// base-data-offset overrides it with an absolute file position.
	dataBase := moof.Offset
	var errs []*fault.Error
	f := r.walkChildren(moof, func(child Header) *fault.Error {
		switch child.Type {
		case "mfhd":
			var mfhd mp4.Mfhd
			if f := r.Unmarshal(child, &mfhd); f != nil {
				return f
			}
			frag.Sequence = mfhd.SequenceNumber
			mfhdSeen = true
		case "traf":
			if trafSeen {
				return fault.New(fault.CodeBadPayload, r.File, child.Offset, "traf", "multiple traf boxes are not supported")
			}
			trafSeen = true
			trafOffset = child.Offset
			return r.walkChildren(child, func(t Header) *fault.Error {
				switch t.Type {
				case "tfhd":
					var tfhd mp4.Tfhd
					if f := r.Unmarshal(t, &tfhd); f != nil {
						return f
					}
					if frag.TrackID != 0 && frag.TrackID != tfhd.TrackID {
						return fault.New(fault.CodeBadPayload, r.File, t.Offset, "tfhd", "track id changed within moof")
					}
					frag.TrackID = tfhd.TrackID
					if tfhd.CheckFlag(mp4.TfhdBaseDataOffsetPresent) {
						dataBase = int64(tfhd.BaseDataOffset)
					} else if !tfhd.CheckFlag(mp4.TfhdDefaultBaseIsMoof) {
						// Unsupported implicit base: fall back to the moof end,
						// which is where a single-traf layout places its data.
						dataBase = moof.End()
					}
					tfhdSeen = true
				case "tfdt":
					var tfdt mp4.Tfdt
					if f := r.Unmarshal(t, &tfdt); f != nil {
						return f
					}
					frag.Tfdt = tfdt.GetBaseMediaDecodeTime()
					frag.TfdtVersion = tfdt.GetVersion()
					frag.TfdtOffset = t.Offset
					tfdtSeen = true
				case "trun":
					var trun mp4.Trun
					if f := r.Unmarshal(t, &trun); f != nil {
						return f
					}
					frag.TrunOffset = t.Offset
					samples, f := r.resolveSamples(t, &trun, dataBase)
					if f != nil {
						return f
					}
					frag.Samples = append(frag.Samples, samples...)
				}
				return nil
			})
		}
		return nil
	})
	if f != nil {
		if frag.Sequence != 0 {
			f = f.WithFragment(frag.Sequence)
		}
		return frag, errs, f
	}
	if !mfhdSeen {
		return nil, errs, fault.New(fault.CodeMissingField, r.File, moof.Offset, "moof", "missing mfhd")
	}
	if !trafSeen || trafOffset < 0 {
		return nil, errs, fault.New(fault.CodeMissingField, r.File, moof.Offset, "moof", "missing traf").WithFragment(frag.Sequence)
	}
	if !tfhdSeen {
		return nil, errs, fault.New(fault.CodeMissingField, r.File, trafOffset, "traf", "missing tfhd").WithFragment(frag.Sequence)
	}
	if !tfdtSeen {
		return nil, errs, fault.New(fault.CodeMissingField, r.File, trafOffset, "traf", "missing tfdt").WithFragment(frag.Sequence)
	}
	if frag.TrackID == 0 {
		return nil, errs, fault.New(fault.CodeMissingField, r.File, trafOffset, "tfhd", "track id must be positive").WithFragment(frag.Sequence)
	}
	if len(frag.Samples) == 0 {
		return nil, errs, fault.New(fault.CodeMissingField, r.File, trafOffset, "traf", "no samples declared").WithFragment(frag.Sequence)
	}

	// The mdat must immediately follow the moof. Trusting the declared moof
	// size is enough to find it, but an mdat that does not start where the
	// moof ends indicates a corrupt layout rather than a resynchronisation
	// point.
	mdat, ok, mf := r.Next(moof.End())
	if mf != nil {
		if frag.Sequence != 0 {
			mf = mf.WithFragment(frag.Sequence)
		}
		return frag, errs, mf
	}
	if !ok || mdat.Type != "mdat" {
		return nil, errs, fault.New(fault.CodeMissingField, r.File, moof.End(), "mdat", "mdat must immediately follow moof").WithFragment(frag.Sequence)
	}
	frag.MdatOffset, frag.MdatSize = mdat.Offset, mdat.Size
	payloadStart := mdat.PayloadStart()
	payloadEnd := mdat.End()
	for i := range frag.Samples {
		s := &frag.Samples[i]
		if s.Offset < payloadStart || s.Offset+s.Size > payloadEnd {
			return nil, errs, fault.New(fault.CodeSampleOutOfRange, r.File, s.DeclOffset, "trun",
				fmt.Sprintf("sample %d declares [%d,%d) outside mdat [%d,%d)", i, s.Offset, s.Offset+s.Size, payloadStart, payloadEnd)).WithFragment(frag.Sequence)
		}
	}
	return frag, errs, nil
}

// resolveSamples computes absolute byte offsets from trun entries relative to
// the trun payload position, per ISO/IEC 14496-12.
func (r *Reader) resolveSamples(trunHeader Header, trun *mp4.Trun, dataBase int64) ([]Sample, *fault.Error) {
	if uint32(len(trun.Entries)) != trun.SampleCount {
		return nil, fault.New(fault.CodeBadPayload, r.File, trunHeader.Offset, "trun",
			fmt.Sprintf("sample_count=%d but %d entries decoded", trun.SampleCount, len(trun.Entries)))
	}
	fields := trunEntryFields(trun)
	if !fields.hasSize {
		return nil, fault.New(fault.CodeMissingField, r.File, trunHeader.Offset, "trun",
			"sample size is required on every trun entry")
	}
	base := dataBase
	if trun.CheckFlag(trunDataOffsetPresent) {
		base += int64(trun.DataOffset)
	}
	entryOffset := trunHeader.PayloadStart() + 4 + 4 // version/flags, sample_count
	if trun.CheckFlag(trunDataOffsetPresent) {
		entryOffset += 4
	}
	if trun.CheckFlag(trunFirstSampleFlagsPresent) {
		entryOffset += 4
	}
	cursor := base
	out := make([]Sample, 0, len(trun.Entries))
	for i, e := range trun.Entries {
		if e.SampleSize == 0 {
			return nil, fault.New(fault.CodeMissingField, r.File, trunHeader.Offset, "trun",
				fmt.Sprintf("entry %d declares zero sample size", i))
		}
		out = append(out, Sample{
			Index:      i,
			Offset:     cursor,
			Size:       int64(e.SampleSize),
			Duration:   int64(e.SampleDuration),
			Cts:        trun.GetSampleCompositionTimeOffset(i),
			DeclOffset: entryOffset + int64(i)*fields.entrySize + fields.sizeOffset,
		})
		cursor += int64(e.SampleSize)
	}
	return out, nil
}

type trunFields struct {
	hasSize    bool
	sizeOffset int64
	entrySize  int64
}

// trunEntryFields derives the serialized layout of one trun entry from the
// trun flags, so declaration offsets can be reported exactly.
func trunEntryFields(trun *mp4.Trun) trunFields {
	var f trunFields
	if trun.CheckFlag(trunSampleDurationPresent) {
		f.entrySize += 4
	}
	if trun.CheckFlag(trunSampleSizePresent) {
		f.hasSize = true
		f.sizeOffset = f.entrySize
		f.entrySize += 4
	}
	if trun.CheckFlag(trunSampleFlagsPresent) {
		f.entrySize += 4
	}
	if trun.CheckFlag(trunCtsOffsetPresent) {
		f.entrySize += 4
	}
	return f
}
