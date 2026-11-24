// Package stream executes the pipeline in bounded windows.
//
// A boundary is committed only after every representation of that segment has
// been rendered and its bytes verified against the recorded digest. On restart
// the runner loads the last valid checkpoint, skips the boundaries it covers,
// and never holds more than one segment's bytes at a time. A bad checkpoint
// falls back to the previous valid boundary; a digest mismatch is refused.
package stream

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	pionrtp "github.com/pion/rtp"

	"mediatool/internal/clock"
	"mediatool/internal/fault"
	"mediatool/internal/index"
	"mediatool/internal/manifest"
	"mediatool/internal/media"
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
	// wroteInit marks which (representation, track) init segments were written.
	wroteInit map[string]bool
}

// emit renders boundaries in order and returns the committed ones.
//
// On resume, the checkpoint's committed refs are revalidated against the
// current alignment. If any ref points at a boundary or representation that no
// longer exists, or a committed ref's digest no longer matches the boundary's
// media span, the runner falls back to the previous valid boundary and
// re-renders from there instead of trusting stale state.
func (r *runner) emit() ([]manifest.Segment, []*fault.Fault, bool, error) {
	faults := append([]*fault.Fault(nil), r.align.Faults...)
	var committed []manifest.Segment
	next := 0

	if r.resumed != nil {
		faults = append(faults, faultRefsToFaults(r.resumed.Faults)...)
		rebuilt, valid := r.revalidate(r.resumed)
		if valid {
			committed = rebuilt
			next = r.resumed.BoundaryIndex + 1
		} else {
			r.fallbacks++
			committed = nil
			next = 0
			faults = append(faults, (&fault.Fault{Kind: fault.KindCheckpoint,
				Recovery: fault.RecoverRestart, Severity: fault.SeverityDamaged}).
				WithDetail("checkpoint boundary %d is not valid against the current alignment; falling back to the start",
					r.resumed.BoundaryIndex))
		}
	}

	done := 0
	for i := range r.align.Segments {
		seg := r.align.Segments[i]
		if seg.Index < next {
			continue
		}
		if r.cfg.StopAfterSegments > 0 && done >= r.cfg.StopAfterSegments {
			return committed, faults, true, nil
		}
		if r.overlapsDamage(seg) {
			// A truncation inside this boundary makes at least one
			// representation unusable. Do not advance the checkpoint past it;
			// the last committed boundary remains the resume point.
			r.fallbacks++
			faults = append(faults, (&fault.Fault{Kind: fault.KindTruncated, Offset: r.damageOffset(seg),
				Recovery: fault.RecoverRestart, Severity: fault.SeverityDamaged}).
				WithDetail("segment %d [%d,%d] overlaps a truncated datagram; checkpoint stays at %d",
					seg.Index, seg.StartMs, seg.EndMs, r.lastCommittedIndex(committed)))
			continue
		}
		refs := make([]manifest.SegmentRef, 0, len(seg.Representations))
		failed := false
		for _, sr := range seg.Representations {
			ref, err := r.renderOne(seg, sr)
			if err != nil {
				if f, ok := fault.As(err); ok && f.Severity < fault.SeverityFatal {
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

// revalidate checks a checkpoint against the current alignment and rebuilds its
// committed segment groups. A ref whose boundary is missing, whose track is not
// in that boundary, or whose digest no longer matches the media span makes the
// whole checkpoint invalid.
func (r *runner) revalidate(cp *Checkpoint) ([]manifest.Segment, bool) {
	if len(cp.Segments) == 0 {
		return nil, cp.BoundaryIndex < 0
	}
	byIndex := make(map[int]segment.Segment, len(r.align.Segments))
	for _, s := range r.align.Segments {
		byIndex[s.Index] = s
	}
	groups := make(map[int]*manifest.Segment)
	for _, ref := range cp.Segments {
		seg, ok := byIndex[ref.Index]
		if !ok {
			return nil, false
		}
		present := false
		var sr segment.Representation
		for _, cand := range seg.Representations {
			// Refs carry the rung ID; coverage IDs are "<rung>/t<track>".
			if cand.TrackID == ref.TrackID && coverageID(ref.RepresentationID, ref.TrackID) == cand.RepresentationID {
				present = true
				sr = cand
				break
			}
		}
		if !present {
			return nil, false
		}
		// Re-render the segment and compare digests. A mismatch means the
		// checkpoint describes bytes this input can no longer produce.
		rep, track := r.repAndTrack(sr)
		data, err := r.render(seg, sr, rep, track)
		if err != nil {
			return nil, false
		}
		digest := index.DigestBytes(data)
		if digest != ref.SHA256 {
			return nil, false
		}
		g := groups[ref.Index]
		if g == nil {
			g = &manifest.Segment{Index: seg.Index, StartMs: seg.StartMs, DurationMs: seg.EndMs - seg.StartMs}
			groups[ref.Index] = g
		}
		g.Refs = append(g.Refs, ref)
	}
	out := make([]manifest.Segment, 0, len(groups))
	for _, s := range r.align.Segments {
		if g, ok := groups[s.Index]; ok {
			out = append(out, *g)
		}
	}
	return out, true
}

func (r *runner) lastCommittedIndex(committed []manifest.Segment) int {
	if len(committed) == 0 {
		return -1
	}
	return committed[len(committed)-1].Index
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
		if err := r.writeInitIfNeeded(rep.ID, sr.TrackID, track); err != nil {
			return manifest.SegmentRef{}, err
		}
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

// writeInitIfNeeded emits one init segment per (representation, track) the
// first time that pair produces media, so the output directory is playable.
func (r *runner) writeInitIfNeeded(repID string, trackID uint32, track TrackSpec) error {
	if r.wroteInit == nil {
		r.wroteInit = make(map[string]bool)
	}
	key := coverageID(repID, trackID)
	if r.wroteInit[key] {
		return nil
	}
	tr, ok := r.in.Index.Track(trackID)
	if !ok {
		return nil
	}
	cfg := media.TrackConfig{
		TrackID: trackID, Kind: track.Kind, ClockRate: tr.ClockRate,
	}
	if track.Video != nil {
		cfg.Width = track.Video.Width
		cfg.Height = track.Video.Height
		cfg.Color = media.ColorBox(track.Video)
	}
	if track.Audio != nil {
		cfg.Channels = track.Audio.Channels
		cfg.SampleRate = track.Audio.SampleRate
	}
	data, err := media.InitSegment(cfg)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s-t%d-init.mp4", sanitize(repID), trackID)
	if err := manifest.WriteAtomic(filepath.Join(r.cfg.OutputDir, name), data); err != nil {
		return err
	}
	r.wroteInit[key] = true
	return nil
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

// render builds the media segment bytes as a fragmented ISO BMFF segment.
//
// The moof carries the sample timing on the shared media timeline and the mdat
// carries the packet bytes the index located. Because the timing and payload
// both come from the verified index and capture, the segment digest
// authenticates the actual output, not an input summary.
func (r *runner) render(seg segment.Segment, sr segment.Representation, rep RepresentationSpec, track TrackSpec) ([]byte, error) {
	samples, err := r.samples(seg, sr, track)
	if err != nil {
		return nil, err
	}
	rate := uint32(90000)
	if track.Audio != nil && track.Audio.SampleRate > 0 {
		rate = uint32(track.Audio.SampleRate)
	} else if tr, ok := r.in.Index.Track(sr.TrackID); ok && tr.ClockRate > 0 {
		rate = tr.ClockRate
	}
	return media.MediaSegment(sr.TrackID, uint32(seg.Index+1), samples, rate)
}

// samples reconstructs the per-packet samples inside a boundary. Only the span
// between this boundary's keyframe and the next is read, so memory stays
// bounded by one segment.
func (r *runner) samples(seg segment.Segment, sr segment.Representation, track TrackSpec) ([]media.Sample, error) {
	tr, ok := r.in.Index.Track(sr.TrackID)
	if !ok {
		return nil, (&fault.Fault{Kind: fault.KindAlignment, TrackID: sr.TrackID,
			Recovery: fault.RecoverAbort, Severity: fault.SeverityFatal}).
			WithDetail("segment %d references missing track %d", seg.Index, sr.TrackID)
	}
	rate := tr.ClockRate
	if track.Audio != nil && track.Audio.SampleRate > 0 {
		rate = uint32(track.Audio.SampleRate)
	}
	if rate == 0 {
		return nil, (&fault.Fault{Kind: fault.KindSampleRate, TrackID: sr.TrackID,
			Recovery: fault.RecoverAbort, Severity: fault.SeverityFatal}).
			WithDetail("track %d has no usable clock rate", sr.TrackID)
	}

	// Read only the packets inside this boundary's byte span, starting at the
	// keyframe offset. This is a direct read of the capture's self-describing
	// records, so memory is bounded by one segment and no full rescan happens.
	startOffset := sr.KeyframeOffset
	endOffset := r.nextKeyframeOffset(sr.TrackID, seg.EndMs)
	limit := int64(r.cfg.maxSegmentBytes())
	if endOffset-startOffset > limit {
		endOffset = startOffset + limit
	}
	if endOffset <= startOffset || r.in.Reader == nil {
		return nil, nil
	}

	packets, err := r.readSpan(sr.TrackID, startOffset, endOffset)
	if err != nil {
		return nil, err
	}

	var out []media.Sample
	var prevRel uint64
	var baseTick uint64
	var have bool
	clk := clock.NewTrackClock(sr.TrackID, 0, clockRateOf(tr))
	for _, p := range packets {
		ext := clk.ExtendRTP(p.ts)
		ticks := ext * uint64(rate) / uint64(clockRateOf(tr))
		if !have {
			baseTick = ticks
		}
		rel := ticks - baseTick
		if have && rel < prevRel {
			rel = prevRel
		}
		var dur uint32
		if have {
			dur = uint32(rel - prevRel)
		}
		out = append(out, media.Sample{
			DurationTicks:   dur,
			DecodeTimeTicks: rel,
			Data:            p.payload,
			Sync:            p.key,
		})
		if have && len(out) >= 2 {
			out[len(out)-2].DurationTicks = dur
		}
		prevRel = rel
		have = true
	}
	// The final sample's duration is unknown until the next boundary; give it
	// the previous interval so the fragment has a consistent duration.
	if n := len(out); n > 0 && out[n-1].DurationTicks == 0 {
		if n >= 2 {
			out[n-1].DurationTicks = out[n-2].DurationTicks
		} else {
			out[n-1].DurationTicks = 1
		}
	}
	return out, nil
}

// recordHeaderLen is the capture framing header size (captured + original).
const recordHeaderLen = 8

// rawPacket is one packet read directly from the capture record framing.
type rawPacket struct {
	ts      uint32
	key     bool
	payload []byte
}

// readSpan reads capture records whose bodies lie in [startBody, endBody),
// keeping only packets of the requested track. Index offsets point at the body
// start, so the record header precedes it by eight bytes.
func (r *runner) readSpan(trackID uint32, startBody, endBody int64) ([]rawPacket, error) {
	var out []rawPacket
	off := startBody - recordHeaderLen
	if off < 0 {
		return nil, fmt.Errorf("bad span start %d", startBody)
	}
	var hdr [8]byte
	for off+recordHeaderLen <= endBody {
		if _, err := r.in.Reader.ReadAt(hdr[:], off); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, nil
			}
			return nil, fmt.Errorf("read record header at %d: %w", off, err)
		}
		captured := int64(binary.BigEndian.Uint32(hdr[0:4]))
		original := int64(binary.BigEndian.Uint32(hdr[4:8]))
		bodyStart := off + recordHeaderLen
		if captured <= 0 || captured > int64(r.cfg.maxSegmentBytes()) || original < captured {
			// A truncated record cannot yield a trustworthy sample; skip it.
			off = bodyStart + captured
			continue
		}
		body := make([]byte, captured)
		if _, err := r.in.Reader.ReadAt(body, bodyStart); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, nil
			}
			return nil, fmt.Errorf("read record body at %d: %w", bodyStart, err)
		}
		off = bodyStart + captured
		if captured < 12 {
			continue
		}
		ssrc := binary.BigEndian.Uint32(body[8:12])
		if tr, ok := r.trackBySSRC(ssrc); !ok || tr.TrackID != trackID {
			continue
		}
		pt := body[1]
		if pt >= 192 && pt <= 223 {
			continue // RTCP, not a media sample
		}
		ts := binary.BigEndian.Uint32(body[4:8])
		payload, ok := samplePayload(body)
		if !ok {
			continue
		}
		key := r.isKeyframePayload(trackID, payload)
		out = append(out, rawPacket{ts: ts, key: key, payload: payload})
	}
	return out, nil
}

// samplePayload extracts the payload from an RTP body, handling header
// extensions and padding. It returns false for an invalid body.
// samplePayload extracts the payload from an RTP body using the same parser as
// the scanner, so extension and padding handling cannot drift between passes.
func samplePayload(body []byte) ([]byte, bool) {
	var pkt pionrtp.Packet
	if err := pkt.Unmarshal(body); err != nil {
		return nil, false
	}
	return append([]byte(nil), pkt.Payload...), true
}

func (r *runner) trackBySSRC(ssrc uint32) (*index.Track, bool) {
	for i := range r.in.Index.Tracks {
		if r.in.Index.Tracks[i].SSRC == ssrc {
			return &r.in.Index.Tracks[i], true
		}
	}
	return nil, false
}

func (r *runner) isKeyframePayload(trackID uint32, payload []byte) bool {
	if tr, ok := r.in.Index.Track(trackID); ok && tr.Kind == "audio" {
		return true
	}
	return len(payload) > 0 && payload[0] == 1
}

func clockRateOf(tr *index.Track) uint32 {
	if tr.ClockRate == 0 {
		return 90000
	}
	return tr.ClockRate
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
