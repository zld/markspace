package job

import (
	"encoding/json"

	"medpipe/internal/clock"
)

const (
	timelineName = "timeline.json"
	indexName    = "track-index.json"
	auditName    = "offset-audit.json"
	summaryName  = "summary.json"
)

type timelineFile struct {
	Schema         string          `json:"schema"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	Tracks         []timelineTrack `json:"tracks"`
}

type timelineTrack struct {
	ID             string           `json:"id"`
	SourceFile     string           `json:"source_file"`
	Timescale      uint32           `json:"timescale"`
	EmptyLeadTicks int64            `json:"empty_edit_lead_ticks"`
	Edits          []editView       `json:"edits"`
	Samples        []timelineSample `json:"samples"`
}

type editView struct {
	Empty                bool           `json:"empty"`
	MediaTime            int64          `json:"media_time"`
	SegmentDurationMovie uint64         `json:"segment_duration_movie_ticks"`
	DurationSeconds      clock.Rational `json:"duration_seconds"`
}

type timelineSample struct {
	Number       int            `json:"sample"`
	DTSTick      int64          `json:"dts_tick"`
	PTSTick      int64          `json:"pts_tick"`
	DTSSeconds   clock.Rational `json:"dts_seconds"`
	PTSSeconds   clock.Rational `json:"pts_seconds"`
	ProgramTick  int64          `json:"program_tick_including_empty_edits"`
	DurationTick int64          `json:"duration_tick"`
	ShiftTicks   int64          `json:"repair_shift_tick"`
	Sync         bool           `json:"sync"`
	ByteOffset   int64          `json:"byte_offset"`
}

type indexFile struct {
	Schema string       `json:"schema"`
	Tracks []indexTrack `json:"tracks"`
}

type indexTrack struct {
	ID          string        `json:"id"`
	SourceFile  string        `json:"source_file"`
	FileSize    int64         `json:"file_size"`
	Timescale   uint32        `json:"timescale"`
	SampleCount int           `json:"sample_count"`
	SyncSamples []int         `json:"sync_samples"`
	Samples     []indexSample `json:"samples"`
}

type indexSample struct {
	Number     int    `json:"sample"`
	ByteOffset int64  `json:"byte_offset"`
	Size       uint32 `json:"size_bytes"`
	Sync       bool   `json:"sync"`
}

type auditFile struct {
	Schema     string            `json:"schema"`
	Repairs    []clock.Repair    `json:"repairs"`
	Isolations []clock.Isolation `json:"isolations"`
	WrapEvents []wrapAuditTrack  `json:"rtp_wrap_events"`
}

type wrapAuditTrack struct {
	Track  string            `json:"track"`
	Events []clock.WrapEvent `json:"events"`
}

func buildTimeline(manifestHash string, results []*TrackResult) ([]byte, error) {
	f := timelineFile{Schema: "medpipe/timeline/v1", ManifestSHA256: manifestHash}
	for _, r := range results {
		tt := timelineTrack{
			ID:         r.Conclusion.ID,
			SourceFile: r.Conclusion.SourceFile,
			Timescale:  r.Scan.MediaTimeBase.TicksPerSecond,
		}
		for _, e := range r.Scan.Edits {
			tt.Edits = append(tt.Edits, editView{
				Empty: e.Empty, MediaTime: e.MediaTime,
				SegmentDurationMovie: e.SegmentDurationTicks, DurationSeconds: e.Duration,
			})
			if e.Empty {
				if ticks, ok := r.Scan.MediaTimeBase.Ticks(e.Duration); ok {
					tt.EmptyLeadTicks += ticks
				}
			}
		}
		for i, s := range r.Samples {
			shift := int64(0)
			if i < len(r.SampleShift) {
				shift = r.SampleShift[i]
			}
			pts := s.PTS + shift
			tt.Samples = append(tt.Samples, timelineSample{
				Number: s.Number, DTSTick: s.DTS, PTSTick: pts,
				DTSSeconds:   r.Scan.MediaTimeBase.Time(s.DTS),
				PTSSeconds:   r.Scan.MediaTimeBase.Time(pts),
				ProgramTick:  tt.EmptyLeadTicks + pts,
				DurationTick: s.Duration, ShiftTicks: shift, Sync: s.Sync,
				ByteOffset: s.ByteOffset,
			})
		}
		f.Tracks = append(f.Tracks, tt)
	}
	return json.MarshalIndent(f, "", "  ")
}

func buildIndex(results []*TrackResult) ([]byte, error) {
	f := indexFile{Schema: "medpipe/track-index/v1"}
	for _, r := range results {
		it := indexTrack{
			ID: r.Conclusion.ID, SourceFile: r.Conclusion.SourceFile,
			FileSize: r.Scan.FileSize, Timescale: r.Scan.MediaTimeBase.TicksPerSecond,
			SampleCount: r.Scan.SampleCount, SyncSamples: r.Scan.SyncSamples,
		}
		for _, s := range r.Samples {
			it.Samples = append(it.Samples, indexSample{
				Number: s.Number, ByteOffset: s.ByteOffset, Size: s.Size, Sync: s.Sync,
			})
		}
		f.Tracks = append(f.Tracks, it)
	}
	return json.MarshalIndent(f, "", "  ")
}

func buildAudit(results []*TrackResult) ([]byte, error) {
	f := auditFile{Schema: "medpipe/offset-audit/v1"}
	for _, r := range results {
		f.Repairs = append(f.Repairs, r.Conclusion.Repairs...)
		if r.Conclusion.Isolation != nil {
			f.Isolations = append(f.Isolations, *r.Conclusion.Isolation)
		}
		if len(r.Conclusion.WrapDetails) > 0 {
			f.WrapEvents = append(f.WrapEvents, wrapAuditTrack{
				Track: r.Conclusion.ID, Events: r.Conclusion.WrapDetails,
			})
		}
	}
	return json.MarshalIndent(f, "", "  ")
}
