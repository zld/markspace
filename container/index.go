package container

import (
	"io"
	"sort"

	mp4 "github.com/abema/go-mp4"

	"mediarc/model"
)

// trackBuilder collects the boxes of one trak before samples are expanded.
type trackBuilder struct {
	id        uint32
	kind      TrackKind
	timescale uint32
	duration  uint64

	chunkOffsets []uint64
	sampleSizes  []uint32
	constSize    uint32
	sampleCount  uint32
	sampleDeltas []uint32
	syncSamples  []uint32
	hasSyncBox   bool
	syncAll      bool

	hasStbl bool
	hasStsz bool
	hasStsc bool
	hasStts bool
	hasStco bool
	stsc    []mp4.StscEntry
}

// structure accumulates index data while walking the moov tree.
type structure struct {
	builders    []*trackBuilder
	tracks      []Track
	divergences []Divergence
	complete    bool
}

// readStructure walks moov and reconstructs tracks from their sample tables.
func readStructure(r io.ReadSeeker, boxes []mp4.BoxInfo) (*structure, error) {
	result := &structure{complete: true}
	moov, ok := findFirst(boxes, mp4.BoxTypeMoov())
	if !ok {
		result.complete = false
		return result, model.New(model.CodeIndexIncomplete, stream, "no moov box found").WithOffset(0)
	}
	moovEnd := int64(moov.Offset + moov.Size)
	traks, err := children(r, int64(moov.Offset)+int64(moov.HeaderSize), moovEnd)
	if err != nil {
		result.complete = false
		return result, model.New(model.CodeIndexIncomplete, stream, "cannot walk moov children").
			WithOffset(int64(moov.Offset)).WithCause(err)
	}
	var firstErr error
	for _, trak := range traks {
		if trak.Type != mp4.BoxTypeTrak() {
			continue
		}
		builder, issues := readTrak(r, trak)
		if len(issues) > 0 {
			result.complete = false
			if firstErr == nil {
				firstErr = issues[0]
			}
			for _, issue := range issues {
				result.divergences = append(result.divergences, Divergence{
					Offset: issue.Offset, Detail: issue.Detail, Err: issue,
				})
			}
		}
		result.builders = append(result.builders, builder)
	}
	if len(result.builders) == 0 {
		result.complete = false
		if firstErr == nil {
			firstErr = model.New(model.CodeIndexIncomplete, stream, "moov carries no tracks")
		}
	}
	sort.SliceStable(result.divergences, func(i, j int) bool {
		return result.divergences[i].Offset < result.divergences[j].Offset
	})
	return result, firstErr
}

// readTrak descends one trak and fills its builder.
func readTrak(r io.ReadSeeker, trak mp4.BoxInfo) (*trackBuilder, []*model.Error) {
	builder := &trackBuilder{}
	end := int64(trak.Offset + trak.Size)
	top, err := children(r, int64(trak.Offset)+int64(trak.HeaderSize), end)
	if err != nil {
		return builder, []*model.Error{model.New(model.CodeIndexIncomplete, stream,
			"cannot walk trak children").WithOffset(int64(trak.Offset)).WithCause(err)}
	}
	issues := make([]*model.Error, 0, 2)
	if tkhd, ok := findFirst(top, mp4.BoxTypeTkhd()); ok {
		var box mp4.Tkhd
		if err := parseBox(r, tkhd, &box); err != nil {
			issues = append(issues, model.New(model.CodeCorruptBox, stream,
				"cannot parse tkhd").WithOffset(int64(tkhd.Offset)).WithCause(err))
		} else {
			builder.id = box.TrackID
			builder.duration = box.GetDuration()
		}
	} else {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			"trak has no tkhd").WithOffset(int64(trak.Offset)))
	}

	mdia, ok := findFirst(top, mp4.BoxTypeMdia())
	if !ok {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			"trak has no mdia").WithOffset(int64(trak.Offset)))
		return builder, issues
	}
	mdiaEnd := int64(mdia.Offset + mdia.Size)
	mid, err := children(r, int64(mdia.Offset)+int64(mdia.HeaderSize), mdiaEnd)
	if err != nil {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			"cannot walk mdia children").WithOffset(int64(mdia.Offset)).WithCause(err))
		return builder, issues
	}
	if hdlr, ok := findFirst(mid, mp4.BoxTypeHdlr()); ok {
		var box mp4.Hdlr
		if err := parseBox(r, hdlr, &box); err != nil {
			issues = append(issues, model.New(model.CodeCorruptBox, stream,
				"cannot parse hdlr").WithOffset(int64(hdlr.Offset)).WithCause(err))
		} else {
			builder.kind = kindOf(box.HandlerType)
		}
	}
	if mdhd, ok := findFirst(mid, mp4.BoxTypeMdhd()); ok {
		var box mp4.Mdhd
		if err := parseBox(r, mdhd, &box); err != nil {
			issues = append(issues, model.New(model.CodeCorruptBox, stream,
				"cannot parse mdhd").WithOffset(int64(mdhd.Offset)).WithCause(err))
		} else {
			builder.timescale = box.Timescale
			builder.duration = box.GetDuration()
		}
	} else {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			"track has no mdhd").WithOffset(int64(mdia.Offset)))
	}
	minf, ok := findFirst(mid, mp4.BoxTypeMinf())
	if !ok {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			"track has no minf").WithOffset(int64(mdia.Offset)))
		return builder, issues
	}
	minfEnd := int64(minf.Offset + minf.Size)
	inf, err := children(r, int64(minf.Offset)+int64(minf.HeaderSize), minfEnd)
	if err != nil {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			"cannot walk minf children").WithOffset(int64(minf.Offset)).WithCause(err))
		return builder, issues
	}
	stbl, ok := findFirst(inf, mp4.BoxTypeStbl())
	if !ok {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			"track has no stbl").WithOffset(int64(minf.Offset)))
		return builder, issues
	}
	builder.hasStbl = true
	stblEnd := int64(stbl.Offset + stbl.Size)
	tables, err := children(r, int64(stbl.Offset)+int64(stbl.HeaderSize), stblEnd)
	if err != nil {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			"cannot walk stbl children").WithOffset(int64(stbl.Offset)).WithCause(err))
		return builder, issues
	}
	builder.readTables(r, tables, &issues)
	return builder, issues
}

func kindOf(handler [4]byte) TrackKind {
	switch string(handler[:]) {
	case "vide":
		return TrackVideo
	case "soun":
		return TrackAudio
	case "subt", "sbtl", "text":
		return TrackSubtitle
	default:
		return TrackOther
	}
}
