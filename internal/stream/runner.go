// Package stream executes the pipeline in bounded windows.
//
// A boundary is committed only after every representation of that segment has
// been rendered and its bytes verified against the recorded digest. On restart
// the runner loads the last valid checkpoint, skips the boundaries it covers,
// and never holds more than one segment's bytes at a time. A bad checkpoint
// falls back to the previous valid boundary; a digest mismatch is refused.
package stream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"mediatool/internal/clock"
	"mediatool/internal/fault"
	"mediatool/internal/index"
	"mediatool/internal/manifest"
	"mediatool/internal/segment"
)

// Config controls execution.
type Config struct {
	// Window bounds how many scanner events are held before the index flushes.
	Window int
	// MaxSegmentBytes bounds one written segment.
	MaxSegmentBytes int
	// CheckpointPath is where the checkpoint is persisted.
	CheckpointPath string
	// OutputDir receives segment files. Empty still renders and digests.
	OutputDir string
	// StopAfterSegments simulates an interruption after N committed segments.
	StopAfterSegments int
	// ResumeFrom is a checkpoint to start from.
	ResumeFrom string
	// Segmenter configures alignment.
	Segmenter segment.Config
}

func (c Config) maxSegmentBytes() int {
	if c.MaxSegmentBytes <= 0 {
		return 1 << 20
	}
	return c.MaxSegmentBytes
}

// Checkpoint is the persisted resume state.
type Checkpoint struct {
	Version         int                   `json:"version"`
	InputDigest     string                `json:"input_digest"`
	BoundaryIndex   int                   `json:"boundary_index"`
	BoundaryStartMs int64                 `json:"boundary_start_ms"`
	BoundaryEndMs   int64                 `json:"boundary_end_ms"`
	Segments        []manifest.SegmentRef `json:"segments"`
	Faults          []manifest.FaultRef   `json:"faults,omitempty"`
	// Fallbacks counts boundaries rolled back because of damage or a bad
	// checkpoint, so resumption is visible in the manifest.
	Fallbacks int    `json:"fallbacks"`
	Checksum  string `json:"checksum"`
}

const checkpointVersion = 1

func (c *Checkpoint) digest() string {
	cp := *c
	cp.Checksum = ""
	b, err := json.Marshal(&cp)
	if err != nil {
		return ""
	}
	return index.DigestBytes(b)
}

// SaveCheckpoint computes the checksum and stores the file atomically.
func SaveCheckpoint(path string, cp *Checkpoint) error {
	cp.Version = checkpointVersion
	cp.Checksum = cp.digest()
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return manifest.WriteAtomic(path, append(data, '\n'))
}

// LoadCheckpoint reads and verifies a checkpoint. A digest mismatch against the
// current input is a fault the CLI maps to exit code 4.
func LoadCheckpoint(path, inputDigest string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint %s: %w", filepath.Base(path), err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, (&fault.Fault{Kind: fault.KindCheckpoint, Recovery: fault.RecoverRestart,
			Severity: fault.SeverityDamaged}).WithDetail("checkpoint %s is unreadable: %v", filepath.Base(path), err)
	}
	if cp.Version != checkpointVersion {
		return nil, (&fault.Fault{Kind: fault.KindCheckpoint, Recovery: fault.RecoverRestart,
			Severity: fault.SeverityDamaged}).WithDetail("checkpoint version %d, want %d", cp.Version, checkpointVersion)
	}
	if got := cp.digest(); got != cp.Checksum {
		return nil, (&fault.Fault{Kind: fault.KindCheckpoint, Recovery: fault.RecoverRestart,
			Severity: fault.SeverityDamaged}).WithDetail("checkpoint checksum mismatch: stored %s computed %s", cp.Checksum, got)
	}
	if inputDigest != "" && cp.InputDigest != inputDigest {
		return nil, (&fault.Fault{Kind: fault.KindCheckpoint, Recovery: fault.RecoverRestart,
			Severity: fault.SeverityDamaged}).
			WithDetail("checkpoint bound to input %s but input is %s; refusing to resume",
				short(cp.InputDigest), short(inputDigest))
	}
	return &cp, nil
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// RepresentationSpec is one rung of the ladder and the tracks it packages.
type RepresentationSpec struct {
	ID      string
	Bitrate int
	Codec   string
	Tracks  []TrackSpec
}

// TrackSpec is the packaging view of one track inside a representation.
type TrackSpec struct {
	TrackID     uint32
	Kind        string
	Audio       *index.AudioInfo
	Video       *index.VideoInfo
	ColorRecord string
}

// Input is the prepared material the runner consumes.
type Input struct {
	Digest          string
	InputBytes      int64
	Index           *index.Index
	Representations []RepresentationSpec
	// Reader supplies the capture bytes by offset. The runner reads only the
	// span of one segment at a time, so memory stays bounded by the segment
	// limit rather than the input size.
	Reader io.ReaderAt
	// TruncatedOffsets lists byte offsets of datagrams whose bodies were cut
	// short. A boundary whose media overlaps one is rolled back.
	TruncatedOffsets []int64
}

// Result reports what a run produced.
type Result struct {
	Manifest        *manifest.Manifest
	Checkpoint      *Checkpoint
	Resumed         bool
	SegmentsWritten int
	AlignmentSkewMs int64
	Faults          []*fault.Fault
	Stopped         bool
	Fallbacks       int
}

// Execute aligns and materializes segments, then builds the manifest.
func Execute(in Input, cfg Config) (*Result, error) {
	if in.Index == nil {
		return nil, fmt.Errorf("stream: nil index")
	}
	res := &Result{}
	var resumed *Checkpoint
	if cfg.ResumeFrom != "" {
		cp, err := LoadCheckpoint(cfg.ResumeFrom, in.Digest)
		if err != nil {
			return nil, err
		}
		resumed = cp
		res.Resumed = true
	}

	alignRes, err := alignAll(in, cfg)
	if err != nil {
		return nil, err
	}

	r := &runner{in: in, cfg: cfg, align: alignRes, resumed: resumed}
	committed, faults, stopped, err := r.emit()
	if err != nil {
		return nil, err
	}

	m := buildManifest(in, alignRes, committed, faults, r.fallbacks)
	res.Manifest = m
	res.SegmentsWritten = len(committed)
	res.AlignmentSkewMs = alignRes.MaxSkewMs
	res.Faults = faults
	res.Stopped = stopped
	res.Fallbacks = r.fallbacks

	cp := &Checkpoint{
		InputDigest: in.Digest,
		Segments:    flatten(committed),
		Faults:      manifestFaults(faults),
		Fallbacks:   r.fallbacks,
	}
	if n := len(committed); n > 0 {
		last := committed[n-1]
		cp.BoundaryIndex = last.Index
		cp.BoundaryStartMs = last.StartMs
		cp.BoundaryEndMs = last.StartMs + last.DurationMs
	} else {
		cp.BoundaryIndex = -1
	}
	res.Checkpoint = cp
	if cfg.CheckpointPath != "" {
		if err := SaveCheckpoint(cfg.CheckpointPath, cp); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// alignAll runs the segmenter over every track of every representation.
func alignAll(in Input, cfg Config) (*segment.Result, error) {
	var covs []segment.Coverage
	for _, rep := range in.Representations {
		for _, t := range rep.Tracks {
			tr, ok := in.Index.Track(t.TrackID)
			if !ok {
				return nil, (&fault.Fault{Kind: fault.KindAlignment, TrackID: t.TrackID,
					Recovery: fault.RecoverAbort, Severity: fault.SeverityFatal}).
					WithDetail("representation %s references missing track %d", rep.ID, t.TrackID)
			}
			covs = append(covs, segment.Coverage{
				ID:      coverageID(rep.ID, t.TrackID),
				Track:   tr,
				Trusted: in.Index.TrustPredicate(t.TrackID),
			})
		}
	}
	return segment.Align(covs, cfg.Segmenter)
}

func coverageID(repID string, trackID uint32) string {
	return fmt.Sprintf("%s/t%d", repID, trackID)
}

type runner struct {
	in        Input
	cfg       Config
	align     *segment.Result
	resumed   *Checkpoint
	fallbacks int
}

// emit renders boundaries in order and returns the committed ones.
func (r *runner) emit() ([]manifest.Segment, []*fault.Fault, bool, error) {
	faults := append([]*fault.Fault(nil), r.align.Faults...)
	var committed []manifest.Segment
	start := 0

	if r.resumed != nil {
		faults = append(faults, faultRefsToFaults(r.resumed.Faults)...)
		// Rebuild committed segments from the checkpoint. If the checkpoint
		// references a boundary the current alignment no longer has, fall back
		// to the previous valid boundary and re-render from there.
		var ok bool
		committed, ok = rebuildCommitted(r.resumed, r.align.Segments)
		if !ok {
			r.fallbacks++
			// Drop to the last boundary whose index exists and is aligned.
			start = fallbackStart(r.resumed, r.align.Segments)
			committed = filterBefore(committed, start)
		}
		if len(committed) > 0 {
			start = committed[len(committed)-1].Index + 1
		}
	}

	done := 0
	for i := range r.align.Segments {
		seg := r.align.Segments[i]
		if seg.Index < start {
			continue
		}
		if r.cfg.StopAfterSegments > 0 && done >= r.cfg.StopAfterSegments {
			return committed, faults, true, nil
		}
		if r.overlapsDamage(seg) {
			// A truncation inside this boundary makes at least one
			// representation unusable; record it and roll back to the previous
			// committed boundary rather than emitting a half-valid segment.
			r.fallbacks++
			faults = append(faults, (&fault.Fault{Kind: fault.KindTruncated, Offset: r.damageOffset(seg),
				Recovery: fault.RecoverRestart, Severity: fault.SeverityDamaged}).
				WithDetail("segment %d [%d,%d] overlaps a truncated datagram; checkpoint stays at %d",
					seg.Index, seg.StartMs, seg.EndMs, seg.Index-1))
			continue
		}
		refs := make([]manifest.SegmentRef, 0, len(seg.Representations))
		failed := false
		for _, sr := range seg.Representations {
			ref, err := r.renderOne(seg, sr)
			if err != nil {
				if f, ok := fault.As(err); ok && f.Severity < fault.SeverityFatal {
					// Recoverable damage: roll back this boundary and continue.
					r.fallbacks++
					faults = append(faults, f)
					failed = true
					break
				}
				return nil, faults, false, err
			}
			refs = append(refs, ref)
		}
		if failed {
			continue
		}
		// Commit only after all representations verified.
		committed = append(committed, manifest.Segment{
			Index:      seg.Index,
			StartMs:    seg.StartMs,
			DurationMs: seg.EndMs - seg.StartMs,
			Refs:       refs,
		})
		done++
	}
	return committed, faults, false, nil
}

// rebuildCommitted reconstructs segment groups from a checkpoint by matching
// each stored ref to the current alignment boundaries.
func rebuildCommitted(cp *Checkpoint, segs []segment.Segment) ([]manifest.Segment, bool) {
	if len(cp.Segments) == 0 {
		return nil, true
	}
	byIndex := make(map[int]*manifest.Segment, len(segs))
	for _, s := range segs {
		s := s
		byIndex[s.Index] = &manifest.Segment{Index: s.Index, StartMs: s.StartMs, DurationMs: s.EndMs - s.StartMs}
	}
	for _, ref := range cp.Segments {
		g, ok := byIndex[ref.Index]
		if !ok {
			return nil, false
		}
		g.Refs = append(g.Refs, ref)
	}
	var out []manifest.Segment
	for _, s := range segs {
		g, ok := byIndex[s.Index]
		if !ok {
			continue
		}
		if len(g.Refs) > 0 {
			out = append(out, *g)
		}
	}
	return out, true
}

func fallbackStart(cp *Checkpoint, segs []segment.Segment) int {
	if cp.BoundaryIndex <= 0 {
		return 0
	}
	for _, s := range segs {
		if s.Index == cp.BoundaryIndex {
			return s.Index
		}
	}
	return 0
}

func filterBefore(segs []manifest.Segment, start int) []manifest.Segment {
	var out []manifest.Segment
	for _, s := range segs {
		if s.Index < start {
			out = append(out, s)
		}
	}
	return out
}

// overlapsDamage reports whether a boundary's media window contains a
// truncated datagram, using each representation's keyframe offset range.
func (r *runner) overlapsDamage(seg segment.Segment) bool {
	if len(r.in.TruncatedOffsets) == 0 {
		return false
	}
	for _, sr := range seg.Representations {
		tr, ok := r.in.Index.Track(sr.TrackID)
		if !ok || tr.Truncated == 0 {
			continue
		}
		for _, off := range r.in.TruncatedOffsets {
			if off >= sr.KeyframeOffset && off < r.nextKeyframeOffset(sr.TrackID, seg.EndMs) {
				return true
			}
		}
	}
	return false
}

func (r *runner) damageOffset(seg segment.Segment) int64 {
	for _, off := range r.in.TruncatedOffsets {
		for _, sr := range seg.Representations {
			if off >= sr.KeyframeOffset && off < r.nextKeyframeOffset(sr.TrackID, seg.EndMs) {
				return off
			}
		}
	}
	return 0
}

func (r *runner) nextKeyframeOffset(trackID uint32, endMs int64) int64 {
	tr, ok := r.in.Index.Track(trackID)
	if !ok {
		return 1 << 62
	}
	if kf, ok := tr.KeyframeAtOrAfter(endMs); ok {
		return kf.Offset
	}
	return 1 << 62
}

// renderOne renders, verifies, optionally writes, and describes one segment.
func (r *runner) renderOne(seg segment.Segment, sr segment.Representation) (manifest.SegmentRef, error) {
	rep, track := r.repAndTrack(sr)
	data, err := r.render(seg, sr, rep, track)
	if err != nil {
		return manifest.SegmentRef{}, err
	}
	if len(data) > r.cfg.maxSegmentBytes() {
		return manifest.SegmentRef{}, (&fault.Fault{Kind: fault.KindAlignment, TrackID: sr.TrackID,
			Offset: sr.KeyframeOffset, Recovery: fault.RecoverAbort, Severity: fault.SeverityFatal}).
			WithDetail("segment %d for %s is %d bytes, above limit %d",
				seg.Index, sr.RepresentationID, len(data), r.cfg.maxSegmentBytes())
	}
	digest := index.DigestBytes(data)
	ref := manifest.SegmentRef{
		RepresentationID: rep.ID,
		TrackID:          sr.TrackID,
		Index:            seg.Index,
		StartMs:          seg.StartMs,
		DurationMs:       seg.EndMs - seg.StartMs,
		KeyframeOffset:   sr.KeyframeOffset,
		KeyframeSeq:      sr.KeyframeSeq,
		SkewMs:           sr.SkewMs,
		ByteLength:       int64(len(data)),
		SHA256:           digest,
	}
	if r.cfg.OutputDir != "" {
		name := fmt.Sprintf("%s-t%d-%05d.m4s", sanitize(rep.ID), sr.TrackID, seg.Index)
		path := filepath.Join(r.cfg.OutputDir, name)
		if err := manifest.WriteAtomic(path, data); err != nil {
			return manifest.SegmentRef{}, err
		}
		onDisk, err := os.ReadFile(path)
		if err != nil {
			return manifest.SegmentRef{}, err
		}
		if got := index.DigestBytes(onDisk); got != digest {
			return manifest.SegmentRef{}, (&fault.Fault{Kind: fault.KindCheckpoint, TrackID: sr.TrackID,
				Offset: sr.KeyframeOffset, Recovery: fault.RecoverRestart, Severity: fault.SeverityFatal}).
				WithDetail("segment %d for %s failed verification: wrote %s read %s",
					seg.Index, sr.RepresentationID, digest, got)
		}
	}
	return ref, nil
}

// repAndTrack resolves a representation and track spec by coverage ID.
func (r *runner) repAndTrack(sr segment.Representation) (RepresentationSpec, TrackSpec) {
	for _, rep := range r.in.Representations {
		if coverageID(rep.ID, sr.TrackID) != sr.RepresentationID {
			continue
		}
		for _, t := range rep.Tracks {
			if t.TrackID == sr.TrackID {
				return rep, t
			}
		}
	}
	return RepresentationSpec{ID: sr.RepresentationID}, TrackSpec{TrackID: sr.TrackID}
}

// render builds the segment bytes. The header pins the input digest, boundary,
// representation identity, and normalized color, so a changed input or an
// unstable choice changes the digest.
func (r *runner) render(seg segment.Segment, sr segment.Representation, rep RepresentationSpec, track TrackSpec) ([]byte, error) {
	color := track.ColorRecord
	if color == "" && track.Video != nil {
		color = track.Video.Color
	}
	if color == "" {
		color = "unknown"
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "MTSG1|rep=%s|track=%d|kind=%s|start=%d|end=%d|kf=%d|skew=%d|color=%s|input=%s\n",
		rep.ID, sr.TrackID, track.Kind, seg.StartMs, seg.EndMs, sr.KeyframeSeq, sr.SkewMs, color, short(r.in.Digest))
	body, err := r.media(seg, sr)
	if err != nil {
		return nil, err
	}
	b.Write(body)
	return b.Bytes(), nil
}

// media reads the bytes between this segment's keyframe and the next, capped
// at the per-segment limit. Only one segment is buffered at a time, so memory
// is bounded by MaxSegmentBytes rather than by the input length.
func (r *runner) media(seg segment.Segment, sr segment.Representation) ([]byte, error) {
	if r.in.Reader == nil {
		return nil, nil
	}
	start := sr.KeyframeOffset
	if start < 0 || start >= r.in.InputBytes {
		return nil, nil
	}
	end := r.nextKeyframeOffset(sr.TrackID, seg.EndMs)
	if end > r.in.InputBytes {
		end = r.in.InputBytes
	}
	if end <= start {
		return nil, nil
	}
	limit := int64(r.cfg.maxSegmentBytes())
	if end-start > limit {
		end = start + limit
	}
	buf := make([]byte, end-start)
	if _, err := r.in.Reader.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read segment bytes at offset %d: %w", start, err)
	}
	return buf, nil
}

// buildManifest assembles the sorted, deterministic output document.
func buildManifest(in Input, alignRes *segment.Result, committed []manifest.Segment, faults []*fault.Fault, fallbacks int) *manifest.Manifest {
	m := manifest.New(in.Digest, in.InputBytes, 0)
	m.Tracks = len(in.Index.Tracks)
	m.Untrusted = toManifestUntrusted(in.Index.Untrusted)
	for _, rep := range in.Representations {
		mr := manifest.Representation{ID: rep.ID, Bitrate: rep.Bitrate, Bandwidth: rep.Bitrate}
		for _, t := range rep.Tracks {
			mr.Tracks = append(mr.Tracks, manifest.TrackRep{
				TrackID: t.TrackID, Kind: t.Kind, Codec: rep.Codec,
				Audio: t.Audio, Video: t.Video, ColorRecord: t.ColorRecord,
			})
		}
		m.Representations = append(m.Representations, mr)
	}
	// Scan faults stay visible even when a resumed run skipped rescanning.
	for _, fr := range in.Index.Faults {
		faults = append(faults, &fault.Fault{Kind: fault.Kind(fr.Kind), TrackID: fr.TrackID,
			Offset: fr.Offset, Recovery: fault.Recovery(fr.Recovery), Severity: fault.SeverityWarning})
	}
	m.Segments = committed
	m.Faults = manifestFaults(faults)
	m.AlignmentSkewMs = alignRes.MaxSkewMs
	m.Checkpoint = manifest.Checkpoint{
		SegmentsCommitted: len(committed),
		InputDigest:       in.Digest,
		Resyncs:           fallbacks,
	}
	if n := len(committed); n > 0 {
		last := committed[n-1]
		m.Checkpoint.LastEndMs = last.StartMs + last.DurationMs
		if len(last.Refs) > 0 {
			m.Checkpoint.LastRepresentation = last.Refs[len(last.Refs)-1].RepresentationID
		}
	}
	m.Checkpoint.Checksum = m.Checkpoint.Digest()
	return m
}

func flatten(segs []manifest.Segment) []manifest.SegmentRef {
	var out []manifest.SegmentRef
	for _, s := range segs {
		out = append(out, s.Refs...)
	}
	return out
}

func sanitize(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c == '/' || c == '\\' || c == ' ' || c == ':' {
			b[i] = '-'
		}
	}
	return string(b)
}

func manifestFaults(fs []*fault.Fault) []manifest.FaultRef {
	out := make([]manifest.FaultRef, 0, len(fs))
	seen := make(map[string]bool, len(fs))
	for _, f := range fs {
		key := fmt.Sprintf("%s/%d/%d", f.Kind, f.TrackID, f.Offset)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, manifest.FaultRef{Kind: string(f.Kind), TrackID: f.TrackID,
			Offset: f.Offset, Recovery: string(f.Recovery), Detail: f.Detail})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func faultRefsToFaults(refs []manifest.FaultRef) []*fault.Fault {
	out := make([]*fault.Fault, 0, len(refs))
	for _, r := range refs {
		out = append(out, &fault.Fault{Kind: fault.Kind(r.Kind), TrackID: r.TrackID,
			Offset: r.Offset, Detail: r.Detail, Recovery: fault.Recovery(r.Recovery),
			Severity: fault.SeverityWarning})
	}
	return out
}

func toManifestUntrusted(us []clock.Untrusted) []manifest.UntrustedInterval {
	out := make([]manifest.UntrustedInterval, 0, len(us))
	for _, u := range us {
		out = append(out, manifest.UntrustedInterval{TrackID: u.TrackID, FromNs: u.FromNs,
			ToNs: u.ToNs, Reason: u.Reason, Offset: u.Offset})
	}
	return out
}
