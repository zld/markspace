package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/manifest"
	"media-audit/internal/media"
	"media-audit/internal/recover"
)

func validateIntegrity(inputs []*parsedInput, item *manifest.Item) {
	for _, in := range inputs {
		integ := in.Doc.Integrity
		if integ == nil {
			continue
		}
		r := integ.Range
		if r.Start < 0 || r.End > int64(len(in.Data)) || r.Start >= r.End {
			e := auditerrors.New(auditerrors.CodeChecksumMismatch, integ.ElementOffset, "integrity range is outside file").
				WithPath(in.Path).WithExpectedActual("valid byte range", fmt.Sprintf("%+v", r))
			item.Errors = append(item.Errors, e)
			continue
		}
		actual := sha256.Sum256(in.Data[r.Start:r.End])
		actualHex := hex.EncodeToString(actual[:])
		if actualHex == integ.Digest {
			continue
		}
		e := auditerrors.New(auditerrors.CodeChecksumMismatch, r.Start, "SHA-256 mismatch over sample range").
			WithPath(in.Path).
			WithBytes(r.Start, r.End).
			WithExpectedActual(integ.Digest, actualHex)
		if loc, ok := recover.LocalizeSingleByte(in.Data, r, integ.Digest); ok {
			e.Offset = loc
			e.Message = fmt.Sprintf("SHA-256 mismatch; single-byte damage localized at %d", loc)
			if tr, smp, found := findSampleAt(in.Doc, loc); found {
				e.WithTrack(tr.ID).WithBytes(smp.RecordStart, smp.RecordEnd)
			}
		}
		item.Errors = append(item.Errors, e)
	}
}

func findSampleAt(doc *media.Document, offset int64) (*media.Track, *media.Sample, bool) {
	for ti := range doc.Tracks {
		tr := &doc.Tracks[ti]
		for si := range tr.Samples {
			s := &tr.Samples[si]
			if offset >= s.RecordStart && offset < s.RecordEnd {
				return tr, s, true
			}
		}
	}
	return nil, nil, false
}
