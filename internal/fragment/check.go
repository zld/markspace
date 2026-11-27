// Package fragment validates fragmented-MP4 boundaries: every moof must
// be paired with a following mdat, trun data offsets must resolve inside
// the paired mdat, sample extents must not overrun it, and each CRC-framed
// sample must verify. Violations report the first offending raw offset.
package fragment

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mediarebuild/internal/model"
	mp4scan "mediarebuild/internal/mp4"
)

// Result of a fragment-boundary check.
type Result struct {
	Fragments int         `json:"fragments"`
	Samples   int         `json:"samples"`
	Diags     model.Diags `json:"diags"`
}

// Check validates all moof/mdat pairs in the MP4 at path. knownTracks
// lists track IDs declared in moov; a tfhd referencing anything else is
// a violation.
func Check(ctx context.Context, path string, knownTracks []uint32) (Result, error) {
	res := Result{}
	scan, err := mp4scan.Scan(ctx, path)
	if err != nil {
		return res, err
	}
	res.Diags = append(res.Diags, scan.Diags...)
	known := map[uint32]bool{}
	for _, id := range knownTracks {
		known[id] = true
	}
	f, err := os.Open(path)
	if err != nil {
		return res, err
	}
	defer f.Close()

	top := topLevel(scan.Boxes)
	for i, b := range top {
		if b.Type != "moof" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res.Fragments++
		if i+1 >= len(top) || top[i+1].Type != "mdat" {
			res.Diags.Add(model.NewDiag(model.StageFragment, "moof-without-mdat", b.Offset, true,
				"moof is not followed by an mdat box"))
			continue
		}
		mdat := top[i+1]
		res.checkPair(f, b, mdat, known)
	}
	return res, nil
}

func topLevel(boxes []mp4scan.Box) []mp4scan.Box {
	var out []mp4scan.Box
	for _, b := range boxes {
		if b.Depth == 0 {
			out = append(out, b)
		}
	}
	return out
}

// checkPair validates one moof against its paired mdat.
func (r *Result) checkPair(f *os.File, moof, mdat mp4scan.Box, known map[uint32]bool) {
	moofInfo := &mp4.BoxInfo{
		Offset:     uint64(moof.Offset),
		Size:       uint64(moof.Size),
		Type:       mp4.BoxTypeMoof(),
		HeaderSize: 8,
	}
	tfhdBoxes, err := mp4.ExtractBoxWithPayload(f, moofInfo,
		mp4.BoxPath{mp4.BoxTypeTraf(), mp4.BoxTypeTfhd()})
	if err != nil || len(tfhdBoxes) == 0 {
		r.Diags.Add(model.NewDiag(model.StageFragment, "tfhd-missing", moof.Offset, true, "moof has no readable tfhd"))
		return
	}
	tfhd := tfhdBoxes[0].Payload.(*mp4.Tfhd)
	if !known[tfhd.TrackID] {
		r.Diags.Add(model.NewDiag(model.StageFragment, "unknown-track", moof.Offset, true,
			fmt.Sprintf("tfhd references undeclared track %d", tfhd.TrackID)))
	}
	trunBoxes, err := mp4.ExtractBoxWithPayload(f, moofInfo,
		mp4.BoxPath{mp4.BoxTypeTraf(), mp4.BoxTypeTrun()})
	if err != nil || len(trunBoxes) == 0 {
		r.Diags.Add(model.NewDiag(model.StageFragment, "trun-missing", moof.Offset, true, "moof has no readable trun"))
		return
	}
	trun := trunBoxes[0].Payload.(*mp4.Trun)
	// data_offset is relative to the start of the enclosing moof.
	dataStart := moof.Offset + int64(trun.DataOffset)
	mdatPayloadStart := mdat.Offset + 8
	mdatEnd := mdat.End()
	if dataStart < mdatPayloadStart || dataStart >= mdatEnd {
		r.Diags.Add(model.NewDiag(model.StageFragment, "bad-data-offset", moof.Offset, true,
			fmt.Sprintf("trun data start %d outside paired mdat [%d,%d)", dataStart, mdatPayloadStart, mdatEnd)))
		return
	}
	pos := dataStart
	for i, e := range trun.Entries {
		end := pos + int64(e.SampleSize)
		if end > mdatEnd {
			d := model.NewDiag(model.StageFragment, "sample-overrun", pos, true,
				fmt.Sprintf("sample %d overruns mdat end %d", i, mdatEnd))
			d.EndOffset = mdatEnd
			r.Diags.Add(d)
			return
		}
		r.Samples++
		r.verifySample(f, pos, int64(e.SampleSize))
		pos = end
	}
}

// verifySample checks one CRC-framed sample at off.
func (r *Result) verifySample(f *os.File, off, size int64) {
	if size < 4 {
		r.Diags.Add(model.NewDiag(model.StageFragment, "sample-too-small", off, true,
			fmt.Sprintf("sample size %d smaller than CRC frame", size)))
		return
	}
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, off); err != nil {
		r.Diags.Add(model.NewDiag(model.StageFragment, "sample-unreadable", off, true, err.Error()))
		return
	}
	want := binary.BigEndian.Uint32(buf)
	got := crc32.ChecksumIEEE(buf[4:])
	if want != got {
		d := model.NewDiag(model.StageFragment, "corrupt-sample", off, true,
			fmt.Sprintf("CRC mismatch: stored %08x, computed %08x", want, got))
		d.EndOffset = off + size
		r.Diags.Add(d)
	}
}
