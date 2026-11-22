package pipeline

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"mediad/internal/clock"
	"mediad/internal/core"
	"mediad/internal/diagnose"
	"mediad/internal/validate"
)

// ReportKind is the discriminator on every JSON Lines record.
type ReportKind string

const (
	KindHeader    ReportKind = "run"
	KindAsset     ReportKind = "asset"
	KindCue       ReportKind = "cue"
	KindRTP       ReportKind = "rtp"
	KindTimecode  ReportKind = "timecode"
	KindDrift     ReportKind = "drift"
	KindTrack     ReportKind = "track_index"
	KindKeyframe  ReportKind = "keyframe"
	KindPoint     ReportKind = "point"
	KindRange     ReportKind = "range"
	KindIssue     ReportKind = "issue"
	KindStage     ReportKind = "stage"
	KindGate      ReportKind = "stage_gate"
	KindInvariant ReportKind = "invariant"
	KindSummary   ReportKind = "summary"
)

// Record is one JSON Lines entry. Exactly one field is populated per Kind.
type Record struct {
	Kind      ReportKind       `json:"kind"`
	Version   int              `json:"version,omitempty"`
	Run       *RunRecord       `json:"run,omitempty"`
	Asset     *AssetRecord     `json:"asset,omitempty"`
	Cue       *CueRecord       `json:"cue,omitempty"`
	RTP       *RTPSummary      `json:"rtp,omitempty"`
	Timecode  *TimecodeRecord  `json:"timecode,omitempty"`
	Drift     *DriftRecord     `json:"drift,omitempty"`
	Track     *TrackRecord     `json:"track,omitempty"`
	Keyframe  *KeyframeRecord  `json:"keyframe,omitempty"`
	Point     *PointRecord     `json:"point,omitempty"`
	Range     *RangeRecord     `json:"range,omitempty"`
	Issue     *IssueRecord     `json:"issue,omitempty"`
	Stage     *StageRecordOut  `json:"stage,omitempty"`
	Gate      *StageCheck      `json:"gate,omitempty"`
	Invariant *InvariantRecord `json:"invariant,omitempty"`
	Summary   *SummaryRecord   `json:"summary,omitempty"`
}

// RunRecord identifies the run and its parameters.
type RunRecord struct {
	Seed         int64    `json:"seed"`
	ClockRate    int64    `json:"clock_rate"`
	MasterTrack  string   `json:"master_track"`
	InputDigest  string   `json:"input_digest"`
	ParamsDigest string   `json:"params_digest"`
	Resumed      bool     `json:"resumed"`
	ReusedStages []string `json:"reused_stages,omitempty"`
}

// AssetRecord carries the identity verify needs to re-check offsets.
type AssetRecord struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// TrackRecord summarizes one keyframe index.
type TrackRecord struct {
	Asset      string        `json:"asset"`
	TrackID    uint32        `json:"track_id"`
	Timescale  uint32        `json:"timescale"`
	FrameCount int           `json:"frame_count"`
	Keyframes  int           `json:"keyframes"`
	Trusted    int           `json:"trusted"`
	Untrusted  int           `json:"untrusted"`
	MinGap     core.Rational `json:"min_gap"`
	MaxGap     core.Rational `json:"max_gap"`
	MeanGap    core.Rational `json:"mean_gap"`
	VFR        bool          `json:"vfr"`
}

// KeyframeRecord is one sync sample and its true forward gap.
type KeyframeRecord struct {
	Asset     string        `json:"asset"`
	TrackID   uint32        `json:"track_id"`
	Timescale uint32        `json:"timescale"`
	Frame     int           `json:"frame"`
	DTS       int64         `json:"dts"`
	Gap       core.Rational `json:"gap"`
	GapTicks  int64         `json:"gap_ticks"`
	Offset    int64         `json:"offset"`
	Span      core.Span     `json:"span"`
	Trusted   bool          `json:"trusted"`
}

// PointRecord is one normalized timeline point.
type PointRecord struct {
	Track   string        `json:"track"`
	Kind    string        `json:"kind"`
	Native  core.Rational `json:"native"`
	Master  core.Rational `json:"master"`
	Wrapped bool          `json:"wrapped"`
	Clamped bool          `json:"clamped"`
	Span    core.Span     `json:"span"`
}

// RangeRecord is one merged half-open damaged interval.
type RangeRecord struct {
	Asset       string    `json:"asset"`
	Format      string    `json:"format"`
	Span        core.Span `json:"span"`
	Categories  []string  `json:"categories"`
	Stages      []string  `json:"stages"`
	Issues      int       `json:"issues"`
	Recoverable bool      `json:"recoverable"`
}

// IssueRecord is one offset-carrying finding.
type IssueRecord struct {
	Category    string    `json:"category"`
	Stage       string    `json:"stage"`
	Format      string    `json:"format"`
	Asset       string    `json:"asset"`
	Span        core.Span `json:"span"`
	Detail      string    `json:"detail,omitempty"`
	Recoverable bool      `json:"recoverable"`
	Node        string    `json:"node,omitempty"`
}

// StageRecordOut exposes a committed stage digest for verification.
type StageRecordOut struct {
	Name           string `json:"name"`
	Digest         string `json:"digest"`
	RecordsThrough int    `json:"records_through"`
}

// InvariantRecord is one validation check in the report.
type InvariantRecord struct {
	Name   string    `json:"name"`
	Passed bool      `json:"passed"`
	Detail string    `json:"detail,omitempty"`
	Stage  string    `json:"stage"`
	Asset  string    `json:"asset,omitempty"`
	Span   core.Span `json:"span"`
}

// SummaryRecord is the last line: counters plus the stable checksum.
type SummaryRecord struct {
	Cues             int    `json:"cues"`
	Frames           int    `json:"frames"`
	KeyframeGaps     int    `json:"keyframe_gaps"`
	CorruptRanges    int    `json:"corrupt_ranges"`
	OffsetErrorBytes int64  `json:"offset_error_bytes"`
	Issues           int    `json:"issues"`
	InvariantsFailed int    `json:"invariants_failed"`
	SHA256           string `json:"sha256"`
}

// WriteJSONL renders an output as canonical JSON Lines. The record order is
// fixed and each line is canonical JSON, so identical runs are byte-identical.
func WriteJSONL(w io.Writer, out *Output, params Parameters, inputDigest, paramsDigest string) error {
	body, err := bodyRecords(out)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	header := Record{Kind: KindHeader, Version: 1, Run: &RunRecord{
		Seed: params.Seed, ClockRate: params.ClockRate, MasterTrack: params.MasterTrack,
		InputDigest: inputDigest, ParamsDigest: paramsDigest,
		Resumed: len(out.Resumed) > 0, ReusedStages: append([]string(nil), out.Resumed...),
	}}
	if err := enc.Encode(header); err != nil {
		return err
	}
	for _, rec := range body {
		if err := enc.Encode(rec); err != nil {
			return err
		}
	}
	s := out.Summary
	if err := enc.Encode(Record{Kind: KindSummary, Summary: &SummaryRecord{
		Cues: s.Cues, Frames: s.Frames, KeyframeGaps: s.KeyframeGaps,
		CorruptRanges: s.CorruptRanges, OffsetErrorBytes: s.OffsetErrorBytes,
		Issues: s.Issues, InvariantsFailed: s.InvariantsFailed, SHA256: s.SHA256,
	}}); err != nil {
		return err
	}
	return bw.Flush()
}

// bodyRecords renders every record covered by the report checksum, in the
// fixed order the report uses. The run header is deliberately excluded: it
// records provenance (which stages were resumed) rather than analysis output,
// so a resumed run must checksum identically to a full run.
func bodyRecords(out *Output) ([]Record, error) {
	var recs []Record
	for i := range out.Assets {
		a := out.Assets[i]
		recs = append(recs, Record{Kind: KindAsset, Asset: &AssetRecord{Name: a.Name, Format: a.Format, Size: a.Size, Digest: a.Digest}})
	}
	if out.Ingest != nil {
		for i := range out.Ingest.Cues {
			c := out.Ingest.Cues[i]
			recs = append(recs, Record{Kind: KindCue, Cue: &c})
		}
		for i := range out.Ingest.RTP {
			r := out.Ingest.RTP[i]
			recs = append(recs, Record{Kind: KindRTP, RTP: &r})
		}
		for i := range out.Ingest.Timecodes {
			t := out.Ingest.Timecodes[i]
			recs = append(recs, Record{Kind: KindTimecode, Timecode: &t})
		}
		for i := range out.Ingest.Drifts {
			d := out.Ingest.Drifts[i]
			recs = append(recs, Record{Kind: KindDrift, Drift: &d})
		}
	}
	if out.Normalize != nil {
		for i := range out.Normalize.Points {
			p := out.Normalize.Points[i]
			recs = append(recs, Record{Kind: KindPoint, Point: &PointRecord{
				Track: p.Track, Kind: string(p.Kind), Native: p.Native, Master: p.Master,
				Wrapped: p.Wrapped, Clamped: p.Clamped, Span: p.Span,
			}})
		}
	}
	if out.Index != nil {
		for i := range out.Index.Indexes {
			idx := &out.Index.Indexes[i]
			recs = append(recs, Record{Kind: KindTrack, Track: &TrackRecord{
				Asset: idx.Asset, TrackID: idx.TrackID, Timescale: idx.Timescale,
				FrameCount: idx.FrameCount, Keyframes: len(idx.Keyframes),
				Trusted: idx.Trusted, Untrusted: idx.Untrusted,
				MinGap: idx.MinGap, MaxGap: idx.MaxGap, MeanGap: idx.MeanGap, VFR: idx.VFR,
			}})
			for _, kf := range idx.Keyframes {
				recs = append(recs, Record{Kind: KindKeyframe, Keyframe: &KeyframeRecord{
					Asset: idx.Asset, TrackID: idx.TrackID, Timescale: idx.Timescale,
					Frame: kf.Number, DTS: kf.DTS, Gap: kf.Gap, GapTicks: kf.GapTicks,
					Offset: kf.Offset, Span: kf.Span, Trusted: kf.Trusted,
				}})
			}
		}
	}
	if out.Diagnose != nil {
		for i := range out.Diagnose.Ranges {
			r := out.Diagnose.Ranges[i]
			recs = append(recs, Record{Kind: KindRange, Range: &RangeRecord{
				Asset: r.Asset, Format: r.Format, Span: r.Span, Categories: r.Categories,
				Stages: r.Stages, Issues: r.Issues, Recoverable: r.Recoverable,
			}})
		}
		for _, is := range out.Diagnose.Issues {
			recs = append(recs, Record{Kind: KindIssue, Issue: &IssueRecord{
				Category: string(is.Category), Stage: string(is.Stage), Format: is.Format,
				Asset: is.Asset, Span: is.Span, Detail: is.Detail,
				Recoverable: is.Recoverable, Node: is.Node,
			}})
		}
	}
	if out.Validate != nil {
		for i := range out.Validate.Gates {
			g := out.Validate.Gates[i]
			recs = append(recs, Record{Kind: KindGate, Gate: &g})
		}
	}
	for _, st := range out.Stages {
		recs = append(recs, Record{Kind: KindStage, Stage: &StageRecordOut{
			Name: st.Name, Digest: st.Digest, RecordsThrough: st.RecordsThrough,
		}})
	}
	if out.Validate != nil {
		for _, c := range out.Validate.Result.Checks {
			recs = append(recs, Record{Kind: KindInvariant, Invariant: &InvariantRecord{
				Name: c.Name, Passed: c.Passed, Detail: c.Detail,
				Stage: string(c.Stage), Asset: c.Asset, Span: c.Span,
			}})
		}
	}
	return recs, nil
}

// ComputeReportChecksum hashes the canonical body records. The summary is not
// part of the digest it carries, and the run header is excluded so a resumed
// run checksums identically to a full one.
func ComputeReportChecksum(out *Output, _ Parameters) (string, error) {
	recs, err := bodyRecords(out)
	if err != nil {
		return "", err
	}
	return bodyDigest(recs)
}

// bodyDigest hashes each canonical body record. Records are hashed as full
// JSON lines so verify can recompute the digest from the report text alone.
func bodyDigest(recs []Record) (string, error) {
	parts := make([][]byte, 0, len(recs))
	for _, rec := range recs {
		data, err := core.CanonicalJSON(rec)
		if err != nil {
			return "", err
		}
		parts = append(parts, data)
	}
	return core.HashParts(parts...), nil
}

// WriteReport writes the JSONL report atomically to path.
func WriteReport(path string, out *Output, params Parameters, inputDigest, paramsDigest string) error {
	if path == "" {
		return errors.New("pipeline: empty report path")
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := WriteJSONL(f, out, params, inputDigest, paramsDigest); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ParsedReport is the reconstruction of a report used by `verify`.
type ParsedReport struct {
	Run         RunRecord
	Assets      []AssetRecord
	Points      []PointRecord
	Tracks      []TrackRecord
	Keyframes   []KeyframeRecord
	Ranges      []RangeRecord
	Issues      []IssueRecord
	Stages      []StageRecordOut
	GatesFailed int
	Invariants  []InvariantRecord
	Summary     SummaryRecord
	Records     int
	// Body holds the canonical form of every checksum-covered record, in
	// report order, so the digest can be recomputed without trusting the
	// stored value.
	Body []Record
}

// ParseReport reads a JSON Lines report, rejecting unknown fields and any
// record that is not structurally valid. Every body record is retained
// verbatim so the checksum can be recomputed from the report text alone.
func ParseReport(r io.Reader) (*ParsedReport, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	out := &ParsedReport{}
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var rec Record
		dec := json.NewDecoder(strings.NewReader(string(line)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("pipeline: report line %d: %w", out.Records+1, err)
		}
		out.Records++
		if rec.Kind != KindHeader && rec.Kind != KindSummary {
			out.Body = append(out.Body, rec)
		}
		switch rec.Kind {
		case KindHeader:
			if rec.Run == nil || rec.Version != 1 {
				return nil, fmt.Errorf("pipeline: report line %d: bad run header", out.Records)
			}
			out.Run = *rec.Run
		case KindAsset:
			if rec.Asset == nil {
				return nil, errors.New("pipeline: empty asset record")
			}
			out.Assets = append(out.Assets, *rec.Asset)
		case KindPoint:
			if rec.Point == nil {
				return nil, errors.New("pipeline: empty point record")
			}
			out.Points = append(out.Points, *rec.Point)
		case KindTrack:
			if rec.Track == nil {
				return nil, errors.New("pipeline: empty track record")
			}
			out.Tracks = append(out.Tracks, *rec.Track)
		case KindKeyframe:
			if rec.Keyframe == nil {
				return nil, errors.New("pipeline: empty keyframe record")
			}
			out.Keyframes = append(out.Keyframes, *rec.Keyframe)
		case KindRange:
			if rec.Range == nil {
				return nil, errors.New("pipeline: empty range record")
			}
			out.Ranges = append(out.Ranges, *rec.Range)
		case KindIssue:
			if rec.Issue == nil {
				return nil, errors.New("pipeline: empty issue record")
			}
			out.Issues = append(out.Issues, *rec.Issue)
		case KindStage:
			if rec.Stage == nil {
				return nil, errors.New("pipeline: empty stage record")
			}
			out.Stages = append(out.Stages, *rec.Stage)
		case KindInvariant:
			if rec.Invariant == nil {
				return nil, errors.New("pipeline: empty invariant record")
			}
			out.Invariants = append(out.Invariants, *rec.Invariant)
		case KindSummary:
			if rec.Summary == nil {
				return nil, errors.New("pipeline: empty summary record")
			}
			out.Summary = *rec.Summary
		case KindGate:
			if rec.Gate == nil {
				return nil, errors.New("pipeline: empty stage gate record")
			}
			if rec.Gate.Failed > 0 {
				out.GatesFailed += rec.Gate.Failed
			}
		case KindCue, KindRTP, KindTimecode, KindDrift:
			// Informational records; verify does not need to re-check them.
		default:
			return nil, fmt.Errorf("pipeline: report line %d: unknown kind %q", out.Records, rec.Kind)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// VerifyReport re-runs invariants from the serialized report alone and
// returns the result plus the recomputed checksum and whether it matches.
func VerifyReport(p *ParsedReport) (*validate.Result, string, bool) {
	sizes := make(map[string]int64, len(p.Assets))
	formats := make(map[string]string, len(p.Assets))
	for _, a := range p.Assets {
		sizes[a.Name] = a.Size
		formats[a.Name] = a.Format
	}
	in := validate.Input{Sizes: sizes, AssetFormats: formats}
	if len(p.Points) > 0 {
		tl := &clock.Timeline{Master: p.Run.MasterTrack}
		for _, pt := range p.Points {
			tl.Points = append(tl.Points, clock.Point{
				Track: pt.Track, Kind: clock.TrackKind(pt.Kind), Native: pt.Native,
				Master: pt.Master, Wrapped: pt.Wrapped, Clamped: pt.Clamped, Span: pt.Span,
			})
		}
		if tl.Master == "" && len(tl.Points) > 0 {
			tl.Master = tl.Points[0].Track
		}
		// Reconstruct track presence so the master lookup can succeed.
		for _, pt := range p.Points {
			found := false
			for _, tr := range tl.Tracks {
				if tr.Name == pt.Track {
					found = true
					break
				}
			}
			if !found {
				tl.Tracks = append(tl.Tracks, clock.Track{Name: pt.Track, Kind: clock.TrackKind(pt.Kind), Asset: pt.Track})
			}
		}
		in.Timeline = tl
	}
	trackIndex := make(map[string]int)
	for _, tr := range p.Tracks {
		key := fmt.Sprintf("%s#%d", tr.Asset, tr.TrackID)
		idx := validate.IndexRef{
			Asset: tr.Asset, TrackID: tr.TrackID, Timescale: tr.Timescale,
			FrameCount: tr.FrameCount, MinGap: tr.MinGap, MaxGap: tr.MaxGap, VFR: tr.VFR,
		}
		trackIndex[key] = len(in.Indexes)
		in.Indexes = append(in.Indexes, idx)
	}
	for _, kf := range p.Keyframes {
		key := fmt.Sprintf("%s#%d", kf.Asset, kf.TrackID)
		at, ok := trackIndex[key]
		if !ok {
			continue
		}
		in.Indexes[at].Keyframes = append(in.Indexes[at].Keyframes, validate.KeyRef{
			Number: kf.Frame, DTS: kf.DTS, Span: kf.Span, Offset: kf.Offset, Gap: kf.Gap, Trusted: kf.Trusted,
		})
	}
	for _, r := range p.Ranges {
		in.Ranges = append(in.Ranges, validate.RangeRef{Asset: r.Asset, Format: r.Format, Span: r.Span})
	}
	for _, is := range p.Issues {
		in.Issues = append(in.Issues, diagnose.Issue{
			Category: diagnose.Category(is.Category), Stage: diagnose.Stage(is.Stage),
			Format: is.Format, Asset: is.Asset, Span: is.Span, Detail: is.Detail,
			Recoverable: is.Recoverable, Node: is.Node,
		})
	}
	res := validate.All(in)
	// A failed per-stage gate is also a verification failure even if the
	// cross-stage checks happen to pass.
	digest, err := bodyDigest(p.Body)
	if err != nil {
		digest = "error"
	}
	ok := digest == p.Summary.SHA256 && p.GatesFailed == 0
	return res, digest, ok
}

// checksumFromStages recomputes the report checksum from committed stage
// digests, using the same fixed stage order as the writer.
func checksumFromStages(stages []StageRecordOut) string {
	byName := make(map[string]string, len(stages))
	for _, st := range stages {
		byName[st.Name] = st.Digest
	}
	parts := make([][]byte, 0, len(StageOrder()))
	for _, name := range StageOrder() {
		d, ok := byName[name]
		if !ok {
			d = "missing"
		}
		parts = append(parts, []byte(name+"\x00"+d))
	}
	return core.HashParts(parts...)
}

// Resummarize recomputes the summary and its checksum after a caller adds
// ground-truth offset checks, so the counters and the report digest stay
// consistent with what WriteJSONL will emit.
func Resummarize(out *Output) Summary {
	s := summarize(out)
	if digest, err := ComputeReportChecksum(out, Parameters{}); err == nil {
		s.SHA256 = digest
	}
	out.Summary = s
	return s
}

// InputDigestOf returns the run input digest, or "" for an incomplete run.
func InputDigestOf(out *Output) string { return out.InputDigest }

// ParamsDigestOf hashes pipeline parameters for the run header.
func ParamsDigestOf(p Parameters) string {
	d, err := paramsDigest(p)
	if err != nil {
		return ""
	}
	return d
}

// summaryChecksum is the checksum definition shared by the writer and verify.
func summaryChecksum(stages []StageDigest) string {
	sorted := append([]StageDigest(nil), stages...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	out := make([]StageRecordOut, 0, len(sorted))
	for _, st := range sorted {
		out = append(out, StageRecordOut{Name: st.Name, Digest: st.Digest, RecordsThrough: st.RecordsThrough})
	}
	return checksumFromStages(out)
}
