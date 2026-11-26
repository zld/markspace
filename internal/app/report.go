package app

import (
	"time"

	"mediaprep/internal/manifest"
	"mediaprep/internal/model"
	"mediaprep/internal/process"
	"mediaprep/internal/process/fingerprint"
	"mediaprep/internal/stream"
)

func buildDocument(
	root string,
	groups []fingerprint.Group,
	segments []process.Segment,
	silences []process.SilenceInterval,
	exceptions []model.Exception,
	metrics stream.Metrics,
) *manifest.Document {
	doc := &manifest.Document{
		SchemaVersion: manifest.Version1,
		Root:          root,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Exceptions:    exceptions,
		Pipeline:      metrics,
	}
	for _, g := range groups {
		a := manifest.Asset{
			Fingerprint: g.Fingerprint, Role: g.Role, Size: g.Size, DuplicateOf: g.DuplicateOf,
		}
		for _, r := range g.Refs {
			a.Sources = append(a.Sources, manifest.SourceRef{Path: r.Path, Track: r.Track, Role: r.Role})
		}
		doc.Assets = append(doc.Assets, a)
	}
	for _, s := range segments {
		doc.Segments = append(doc.Segments, manifest.Segment{
			ID: s.ID, Track: s.Track, Role: s.Role, Index: s.Index,
			Interval: manifest.Interval{
				Start: s.Interval.Start, End: s.Interval.End, Scale: s.Interval.Scale,
			},
			FirstSample: s.FirstSample, EndSample: s.EndSample, ByteSize: s.ByteSize,
		})
	}
	for _, s := range silences {
		doc.Silences = append(doc.Silences, manifest.Silence{
			Track: s.Track,
			Interval: manifest.Interval{
				Start: s.Interval.Start, End: s.Interval.End, Scale: s.Interval.Scale,
			},
			DurationMS: s.DurationMS, RMSDB: s.RMSDB, RMSDBText: s.RMSDBText,
			PeakDBFS: s.PeakDBFS, WindowCount: s.WindowCount,
		})
	}
	return doc
}
