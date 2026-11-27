// Package track validates MP4 track structure against the shared
// timebase and error model: timescale sanity, unique track IDs, sample
// table consistency (stts vs stsz) and chunk offsets within the file.
package track

import (
	"context"
	"fmt"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mediarebuild/internal/model"
)

// Info summarises one validated track.
type Info struct {
	TrackID     uint32 `json:"track_id"`
	Timescale   uint32 `json:"timescale"`
	SampleCount uint32 `json:"sample_count"`
}

// Result of validating all tracks in a container.
type Result struct {
	Tracks []Info      `json:"tracks"`
	Diags  model.Diags `json:"diags"`
}

// extractPayloads pulls all boxes at path under moov.
func extractPayloads(f *os.File, path mp4.BoxPath) ([]*mp4.BoxInfoWithPayload, error) {
	return mp4.ExtractBoxWithPayload(f, nil, path)
}

// Validate parses the moov box of the MP4 at path and checks every track.
// fileSize is used to bounds-check chunk offsets.
func Validate(ctx context.Context, path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	res := Result{}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	traks, err := extractPayloads(f, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak()})
	if err != nil {
		res.Diags.Add(model.NewDiag(model.StageValidate, "moov-missing", 0, false, err.Error()))
		return res, nil
	}
	if len(traks) == 0 {
		res.Diags.Add(model.NewDiag(model.StageValidate, "no-tracks", 0, false, "container has no tracks"))
		return res, nil
	}
	seenIDs := map[uint32]bool{}
	for _, trak := range traks {
		info, diags := validateTrack(f, trak, fi.Size(), seenIDs)
		res.Diags = append(res.Diags, diags...)
		if info != nil {
			res.Tracks = append(res.Tracks, *info)
		}
	}
	return res, nil
}

// findNested walks trak/mdia and trak/mdia/minf/stbl for a box type.
func findNested(f *os.File, trak *mp4.BoxInfoWithPayload, path ...mp4.BoxType) (*mp4.BoxInfoWithPayload, bool) {
	boxes, err := mp4.ExtractBoxWithPayload(f, &trak.Info, mp4.BoxPath(path))
	if err != nil || len(boxes) == 0 {
		return nil, false
	}
	return boxes[0], true
}

func validateTrack(f *os.File, trak *mp4.BoxInfoWithPayload, fileSize int64, seen map[uint32]bool) (*Info, model.Diags) {
	var diags model.Diags
	trakOff := int64(trak.Info.Offset)

	tkhdBox, ok := findNested(f, trak, mp4.BoxTypeTkhd())
	if !ok {
		diags.Add(model.NewDiag(model.StageValidate, "tkhd-missing", trakOff, false, "track has no tkhd"))
		return nil, diags
	}
	tkhd := tkhdBox.Payload.(*mp4.Tkhd)
	if seen[tkhd.TrackID] {
		diags.Add(model.NewDiag(model.StageValidate, "dup-track-id", int64(tkhdBox.Info.Offset), false,
			fmt.Sprintf("duplicate track id %d", tkhd.TrackID)))
		return nil, diags
	}
	seen[tkhd.TrackID] = true

	mdhdBox, ok := findNested(f, trak, mp4.BoxTypeMdia(), mp4.BoxTypeMdhd())
	if !ok {
		diags.Add(model.NewDiag(model.StageValidate, "mdhd-missing", trakOff, false, "track has no mdhd"))
		return nil, diags
	}
	mdhd := mdhdBox.Payload.(*mp4.Mdhd)
	if mdhd.Timescale == 0 {
		// Without a timescale no time mapping can be established: fatal.
		diags.Add(model.NewDiag(model.StageValidate, "zero-timescale", int64(mdhdBox.Info.Offset), false,
			fmt.Sprintf("track %d has timescale 0; cannot map media time", tkhd.TrackID)))
		return nil, diags
	}

	stbl := mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl()}
	sttsBox, okT := findNested(f, trak, append(stbl, mp4.BoxTypeStts())...)
	stszBox, okZ := findNested(f, trak, append(stbl, mp4.BoxTypeStsz())...)
	stcoBox, okC := findNested(f, trak, append(stbl, mp4.BoxTypeStco())...)
	if !okT || !okZ || !okC {
		diags.Add(model.NewDiag(model.StageValidate, "incomplete-index", trakOff, true,
			fmt.Sprintf("track %d: sample table incomplete (stts=%v stsz=%v stco=%v)",
				tkhd.TrackID, okT, okZ, okC)))
		return &Info{TrackID: tkhd.TrackID, Timescale: mdhd.Timescale}, diags
	}
	stts := sttsBox.Payload.(*mp4.Stts)
	stsz := stszBox.Payload.(*mp4.Stsz)
	stco := stcoBox.Payload.(*mp4.Stco)

	var sttsTotal uint64
	for _, e := range stts.Entries {
		sttsTotal += uint64(e.SampleCount)
	}
	if uint64(stsz.SampleCount) != sttsTotal {
		diags.Add(model.NewDiag(model.StageValidate, "sample-count-mismatch", int64(stszBox.Info.Offset), true,
			fmt.Sprintf("track %d: stts totals %d samples but stsz declares %d",
				tkhd.TrackID, sttsTotal, stsz.SampleCount)))
	}
	if uint32(len(stsz.EntrySize)) != stsz.SampleCount && stsz.SampleSize == 0 {
		diags.Add(model.NewDiag(model.StageValidate, "incomplete-index", int64(stszBox.Info.Offset), true,
			fmt.Sprintf("track %d: stsz declares %d samples but lists %d entries",
				tkhd.TrackID, stsz.SampleCount, len(stsz.EntrySize))))
	}
	for _, off := range stco.ChunkOffset {
		if int64(off) >= fileSize {
			diags.Add(model.NewDiag(model.StageValidate, "chunk-out-of-range", int64(stcoBox.Info.Offset), true,
				fmt.Sprintf("track %d: chunk offset %d beyond file size %d", tkhd.TrackID, off, fileSize)))
			break
		}
	}
	return &Info{TrackID: tkhd.TrackID, Timescale: mdhd.Timescale, SampleCount: stsz.SampleCount}, diags
}
