package scan

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/abema/go-mp4"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// box is one top-level ISO BMFF box with its file offsets.
type box struct {
	typ    string
	offset int64
	size   int64
	header int64
}

// walkTopLevel enumerates top-level boxes. A box whose declared size passes the
// end of the segment stops the walk and is reported as a truncation point.
func walkTopLevel(data []byte) (boxes []box, badOffset int64, ok bool) {
	var off int64
	for off < int64(len(data)) {
		if int64(len(data))-off < 8 {
			return boxes, off, false
		}
		size := int64(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		header := int64(8)
		switch size {
		case 1:
			if int64(len(data))-off < 16 {
				return boxes, off, false
			}
			size = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
			header = 16
		case 0:
			size = int64(len(data)) - off
		}
		if size < header {
			return boxes, off, false
		}
		boxes = append(boxes, box{typ: typ, offset: off, size: size, header: header})
		if off+size > int64(len(data)) {
			return boxes, off, false
		}
		off += size
	}
	return boxes, 0, true
}

type fragmentInfo struct {
	trun       *mp4.Trun
	tfhd       *mp4.Tfhd
	tfdt       *mp4.Tfdt
	sampleSum  int64
	sampleCnt  int
	firstFlags uint32
	haveFirst  bool
}

func inspectMP4(req Request, data []byte) (string, MediaInfo, fault.List) {
	var issues fault.List
	info := MediaInfo{Kind: "mp4"}
	boxes, badOffset, ok := walkTopLevel(data)
	if !ok {
		issues = append(issues, fault.Issue{
			Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: "container box extends past the end of the segment",
			Source: fault.Source{
				Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
				Loc:   fault.Locator{File: req.Path, Offset: badOffset},
				Start: req.Start, End: req.End,
			},
		})
	}

	var moof *box
	mdatPayload := int64(-1)
	var mdatOffset int64
	for i := range boxes {
		b := &boxes[i]
		switch b.typ {
		case "moof":
			if moof == nil {
				moof = b
			}
		case "mdat":
			mdatOffset = b.offset
			if b.offset+b.size > int64(len(data)) {
				issues = append(issues, fault.Issue{
					Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
					Message: fmt.Sprintf("mdat declares %d bytes but only %d remain", b.size, int64(len(data))-b.offset),
					Source: fault.Source{
						Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
						Loc:   fault.Locator{File: req.Path, Offset: b.offset},
						Start: req.Start, End: req.End,
					},
				})
				mdatPayload = int64(len(data)) - (b.offset + b.header)
			} else {
				mdatPayload = b.size - b.header
			}
		}
	}

	fs := readFragment(req, data, moof, &issues)
	info.SampleCount = fs.sampleCnt
	info.PayloadBytes = fs.sampleSum
	if fs.tfhd != nil {
		info.TrackID = fs.tfhd.TrackID
	}
	if fs.tfdt != nil {
		info.Timescale = timescaleFromInit(req.InitPath)
		if t, err := clock.FromTimescale(int64(fs.tfdt.GetBaseMediaDecodeTime()), info.Timescale); err == nil {
			info.BaseDecode = t
			if !req.Start.Equal(clock.Zero) && !t.Equal(req.Start) {
				issues = append(issues, boundaryIssue(req, fault.SeverityError, fault.CodeSegmentTruncated,
					fmt.Sprintf("fragment decodes at %s but the manifest places it at %s", t, req.Start)))
			}
		}
	}
	if moof != nil && mdatPayload < 0 {
		issues = append(issues, fault.Issue{
			Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: "fragment declares samples but has no mdat payload",
			Source: fault.Source{
				Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
				Loc:   fault.Locator{File: req.Path, Offset: moof.offset},
				Start: req.Start, End: req.End,
			},
		})
	}
	if fs.sampleSum > 0 && mdatPayload >= 0 && fs.sampleSum > mdatPayload {
		issues = append(issues, fault.Issue{
			Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: fmt.Sprintf("fragment references %d sample bytes but mdat holds %d", fs.sampleSum, mdatPayload),
			Source: fault.Source{
				Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
				Loc:   fault.Locator{File: req.Path, Offset: moof.offset},
				Start: req.Start, End: req.End,
			},
		})
	}
	_ = mdatOffset
	info.Keyframe = fs.haveFirst && fs.firstFlags&0x00010000 == 0
	if req.Discontinuity {
		switch {
		case moof == nil:
			issues = append(issues, boundaryIssue(req, fault.SeverityError, fault.CodeDiscontinuity,
				"segment after a discontinuity is not a self-contained fragment"))
		case fs.haveFirst && !info.Keyframe:
			// Only assert keyframe alignment when the fragment actually
			// declares sample flags; absence of flags is not evidence.
			issues = append(issues, boundaryIssue(req, fault.SeverityError, fault.CodeDiscontinuity,
				"first sample after a discontinuity is not an independent keyframe"))
		}
	}
	return "mp4", info, issues
}

// readFragment extracts trun/tfhd/tfdt with go-mp4.
func readFragment(req Request, data []byte, moof *box, issues *fault.List) fragmentInfo {
	var fs fragmentInfo
	if moof == nil {
		return fs
	}
	r := bytes.NewReader(data)
	bi := &mp4.BoxInfo{
		Offset: uint64(moof.offset), Size: uint64(moof.size),
		HeaderSize: uint64(moof.header), Type: mp4.BoxTypeMoof(),
	}
	// Paths are relative to the moof parent box that ExtractBoxesWithPayload
	// starts from.
	paths := []mp4.BoxPath{
		{mp4.BoxTypeTraf(), mp4.BoxTypeTrun()},
		{mp4.BoxTypeTraf(), mp4.BoxTypeTfhd()},
		{mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()},
	}
	boxes, err := mp4.ExtractBoxesWithPayload(r, bi, paths)
	if err != nil {
		*issues = append(*issues, fault.Issue{
			Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: fmt.Sprintf("fragment metadata unreadable: %v", err),
			Source: fault.Source{
				Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
				Loc:   fault.Locator{File: req.Path, Offset: moof.offset},
				Start: req.Start, End: req.End,
			},
		})
		return fs
	}
	var defaultSize uint32
	haveDefaultSize := false
	for _, b := range boxes {
		switch v := b.Payload.(type) {
		case *mp4.Trun:
			fs.trun = v
		case *mp4.Tfhd:
			fs.tfhd = v
			if v.GetFlags()&mp4.TfhdDefaultSampleSizePresent != 0 {
				defaultSize = v.DefaultSampleSize
				haveDefaultSize = true
			}
		case *mp4.Tfdt:
			fs.tfdt = v
		}
	}
	if fs.trun == nil {
		*issues = append(*issues, fault.Issue{
			Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: "moof has no traf/trun sample table",
			Source: fault.Source{
				Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
				Loc:   fault.Locator{File: req.Path, Offset: moof.offset},
				Start: req.Start, End: req.End,
			},
		})
		return fs
	}
	fs.sampleCnt = int(fs.trun.SampleCount)
	hasSize := fs.trun.GetFlags()&0x200 != 0
	for _, e := range fs.trun.Entries {
		switch {
		case hasSize:
			fs.sampleSum += int64(e.SampleSize)
		case haveDefaultSize:
			fs.sampleSum += int64(defaultSize)
		}
	}
	if !hasSize && !haveDefaultSize && fs.trun.SampleCount > 0 {
		*issues = append(*issues, fault.Issue{
			Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: "trun carries no sample sizes and tfhd has no default size",
			Source: fault.Source{
				Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
				Loc:   fault.Locator{File: req.Path, Offset: int64(boxes[0].Info.Offset)},
				Start: req.Start, End: req.End,
			},
		})
	}
	if len(fs.trun.Entries) > 0 || fs.trun.GetFlags()&0x004 != 0 {
		if fs.trun.GetFlags()&0x004 != 0 {
			fs.firstFlags = fs.trun.FirstSampleFlags
			fs.haveFirst = true
		} else if fs.trun.GetFlags()&0x400 != 0 {
			fs.firstFlags = fs.trun.Entries[0].SampleFlags
			fs.haveFirst = true
		}
	}
	if int64(fs.sampleCnt) != int64(len(fs.trun.Entries)) && !hasSize && fs.trun.GetFlags()&0x100 == 0 {
		// Sample count without per-entry data is legal only with tfhd defaults;
		// without both the duration/size totals are unknowable.
		fs.sampleCnt = len(fs.trun.Entries)
	}
	return fs
}

// timescaleFromInit reads the media timescale from the init segment. When the
// init is unavailable the generator's default timescale of 1000 is used.
func timescaleFromInit(path string) uint32 {
	const fallback = 1000
	if path == "" {
		return fallback
	}
	f, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer f.Close()
	boxes, err := mp4.ExtractBoxesWithPayload(f, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
	})
	if err != nil || len(boxes) == 0 {
		return fallback
	}
	if mdhd, ok := boxes[0].Payload.(*mp4.Mdhd); ok && mdhd.Timescale > 0 {
		return mdhd.Timescale
	}
	return fallback
}
