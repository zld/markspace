package container

import (
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediarc/model"
)

// readTables decodes the sample tables that are present.
func (b *trackBuilder) readTables(r io.ReadSeeker, tables []mp4.BoxInfo, issues *[]*model.Error) {
	addIssue := func(code model.Code, info mp4.BoxInfo, detail string, err error) {
		e := model.New(code, stream, detail).WithOffset(int64(info.Offset))
		if err != nil {
			e = e.WithCause(err)
		}
		*issues = append(*issues, e)
	}
	if info, ok := findFirst(tables, mp4.BoxTypeStsz()); ok {
		var box mp4.Stsz
		if err := parseBox(r, info, &box); err != nil {
			addIssue(model.CodeCorruptBox, info, "cannot parse stsz", err)
		} else {
			b.hasStsz = true
			b.constSize = box.SampleSize
			b.sampleCount = box.SampleCount
			b.sampleSizes = box.EntrySize
		}
	}
	if info, ok := findFirst(tables, mp4.BoxTypeStts()); ok {
		var box mp4.Stts
		if err := parseBox(r, info, &box); err != nil {
			addIssue(model.CodeCorruptBox, info, "cannot parse stts", err)
		} else {
			b.hasStts = true
			for _, entry := range box.Entries {
				for i := uint32(0); i < entry.SampleCount; i++ {
					b.sampleDeltas = append(b.sampleDeltas, entry.SampleDelta)
					if len(b.sampleDeltas) > DefaultMaxSamplesPerTrack*4 {
						addIssue(model.CodeIndexIncomplete, info, "stts expands past the sample cap", nil)
						return
					}
				}
			}
		}
	}
	if info, ok := findFirst(tables, mp4.BoxTypeStsc()); ok {
		var box mp4.Stsc
		if err := parseBox(r, info, &box); err != nil {
			addIssue(model.CodeCorruptBox, info, "cannot parse stsc", err)
		} else {
			b.hasStsc = true
			b.stsc = box.Entries
		}
	}
	if info, ok := findFirst(tables, mp4.BoxTypeStco()); ok {
		var box mp4.Stco
		if err := parseBox(r, info, &box); err != nil {
			addIssue(model.CodeCorruptBox, info, "cannot parse stco", err)
		} else {
			b.hasStco = true
			for _, offset := range box.ChunkOffset {
				b.chunkOffsets = append(b.chunkOffsets, uint64(offset))
			}
		}
	} else if info, ok := findFirst(tables, mp4.BoxTypeCo64()); ok {
		var box mp4.Co64
		if err := parseBox(r, info, &box); err != nil {
			addIssue(model.CodeCorruptBox, info, "cannot parse co64", err)
		} else {
			b.hasStco = true
			b.chunkOffsets = append(b.chunkOffsets, box.ChunkOffset...)
		}
	}
	if info, ok := findFirst(tables, mp4.BoxTypeStss()); ok {
		var box mp4.Stss
		if err := parseBox(r, info, &box); err != nil {
			addIssue(model.CodeCorruptBox, info, "cannot parse stss", err)
		} else {
			b.hasSyncBox = true
			b.syncSamples = box.SampleNumber
		}
	}
	if b.kind == TrackAudio {
		b.syncAll = true
	}
}
