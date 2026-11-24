// Package pipeline wires the media-processing kernel together: a single
// streaming pass resolves PSI snapshots, repaired timestamps and elementary
// access units; segmentation aligns output to keyframes; a DAG then drives
// remux, per-segment verification and provenance.
//
// The pass is streaming on purpose. Only the access-unit citations (offsets,
// timestamps, hashes) are retained, so the 2 GiB single-file limit is enforced
// by the scanner rather than by available memory.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"tsforge/internal/clock"
	"tsforge/internal/dag"
	"tsforge/internal/index"
	"tsforge/internal/provenance"
	"tsforge/internal/remux"
	"tsforge/internal/ts"
)

// MaxCacheEntries bounds the DAG result cache for one run.
const MaxCacheEntries = 4096

// Params are the operator-visible processing options. They are folded into
// every DAG key, so changing any of them invalidates cached results.
type Params struct {
	Seed             int64        `json:"seed"`
	SegmentTicks     int64        `json:"segment_ticks"`
	KeyframeOnly     bool         `json:"keyframe_only"`
	IndexLimit       int          `json:"index_limit"`
	MaxFileBytes     int64        `json:"max_file_bytes"`
	MaxPackets       int64        `json:"max_packets"`
	RepairTimestamps bool         `json:"repair_timestamps"`
	MaxSegments      int          `json:"max_segments"`
	Clock            clock.Config `json:"-"`
}

// DefaultParams returns demo-scale defaults: 5-second segments.
func DefaultParams() Params {
	return Params{
		Seed:             17,
		SegmentTicks:     5 * clock.TimeScale,
		IndexLimit:       index.MaxRecords,
		MaxFileBytes:     ts.MaxFileBytes,
		MaxPackets:       ts.MaxPackets,
		RepairTimestamps: true,
		MaxSegments:      32,
		Clock:            clock.DefaultConfig(),
	}
}

// Sample is one elementary access unit after PES grouping.
type Sample struct {
	PID          uint16
	Kind         index.Kind
	Offsets      []int64 // every packet carrying this access unit
	FirstPacket  int64
	DTS          int64
	PTS          int64
	Keyframe     bool
	RandomAccess bool
	SnapshotID   string
	PayloadBytes int
	PayloadHash  string
}

// Analysis is the result of the streaming pass.
type Analysis struct {
	InputPath     string
	InputHash     string
	InputSize     int64
	Snapshots     []*ts.Snapshot
	Diagnostics   []ts.Diagnostic
	ClockErrors   clock.ErrorList
	Stats         ts.Stats
	Samples       []Sample
	VideoPID      uint16
	AudioPID      uint16
	FirstVideoDTS int64
	LastVideoDTS  int64
	Errors        []provenance.ErrorRecord
}

type assembler struct {
	psi     *ts.PSI
	unwrap  *clock.Unwrapper
	params  Params
	samples []Sample
	current map[uint16]*Sample
	// snapshot in force when a sample's first packet was seen is captured
	// per access unit, so a later PSI change never rewrites history.
	snapAt     map[string]*ts.Snapshot
	payloads   *payloadReader
	limitHit   bool
	maxSamples int
}

func (a *assembler) feed(p *ts.Packet) error {
	if p.Duplicate || p.TransportError {
		return nil
	}
	a.psi.Feed(p)
	snap := a.psi.Snapshot()
	if snap == nil {
		return nil
	}
	if _, known := a.snapAt[snap.ID]; !known {
		a.snapAt[snap.ID] = snap.Copy()
	}

	// PCR observations feed the clock model independently of PES timing.
	if p.Adaptation.PCR {
		_, _ = a.unwrap.Observe(clock.Obs{
			PID: p.PID, Kind: clock.PCR, Raw: a.pcr27(p.Adaptation.PCRBase, p.Adaptation.PCRExtension),
			Offset: p.Offset, Packet: p.Index,
			Discontinuity: p.Adaptation.Discontinuity,
		})
	}
	st, ok := snap.StreamByPID(p.PID)
	if !ok {
		return nil
	}
	kind := index.KindOther
	lane := clock.LaneUnknown
	switch st.Kind {
	case ts.StreamVideo:
		kind, lane = index.KindVideo, clock.LaneVideo
	case ts.StreamAudio:
		kind, lane = index.KindAudio, clock.LaneAudio
	}
	if kind == index.KindOther {
		return nil
	}
	a.unwrap.SetLane(p.PID, lane)

	if !p.PayloadStart {
		if cur := a.current[p.PID]; cur != nil && cur.SnapshotID == snap.ID {
			cur.Offsets = append(cur.Offsets, p.Offset)
			cur.PayloadBytes += len(p.Payload)
			cur.PayloadHash = "" // invalidated; recomputed at close
		}
		return nil
	}
	pes, err := ts.ParsePES(p.Payload)
	if err != nil || !pes.HasPTS {
		return nil
	}
	// Close the previous access unit of this PID first: its hashing covers
	// the complete elementary payload assembled so far.
	if prev := a.current[p.PID]; prev != nil {
		a.close(prev)
	}
	ticks, ok := a.unwrap.Observe(clock.Obs{
		PID: p.PID, Kind: clock.PTS, Raw: uint64(pes.PTS), Lane: lane,
		Offset: p.Offset, Packet: p.Index,
	})
	if !ok {
		return nil
	}
	dtsTicks := ticks
	if pes.HasDTS {
		v, ok := a.unwrap.Observe(clock.Obs{
			PID: p.PID, Kind: clock.DTS, Raw: uint64(pes.DTS), Lane: lane,
			Offset: p.Offset, Packet: p.Index,
		})
		if !ok {
			return nil
		}
		dtsTicks = v
	}
	a.current[p.PID] = &Sample{
		PID: p.PID, Kind: kind,
		Offsets:      []int64{p.Offset},
		FirstPacket:  p.Index,
		PTS:          ticks,
		DTS:          dtsTicks,
		Keyframe:     p.Adaptation.RandomAccess,
		RandomAccess: p.Adaptation.RandomAccess,
		SnapshotID:   snap.ID,
		PayloadBytes: len(p.Payload),
	}
	_ = st
	return nil
}

// pcr27 folds the PCR base and extension into a 27 MHz value.
func (a *assembler) pcr27(base uint64, ext uint16) uint64 {
	return base*300 + uint64(ext)
}

func (a *assembler) close(s *Sample) {
	if a.maxSamples > 0 && len(a.samples) >= a.maxSamples {
		a.limitHit = true
		return
	}
	a.samples = append(a.samples, *s)
}

// Analyze performs the streaming pass over inputPath.
func Analyze(ctx context.Context, inputPath string, params Params) (*Analysis, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if params.SegmentTicks <= 0 {
		params.SegmentTicks = DefaultParams().SegmentTicks
	}
	if params.IndexLimit <= 0 {
		params.IndexLimit = index.MaxRecords
	}
	sc, err := ts.NewScanner(inputPath, ts.Config{
		MaxFileBytes: params.MaxFileBytes, MaxPackets: params.MaxPackets, StrictPayload: true,
	})
	if err != nil {
		return nil, err
	}
	defer sc.Close()
	hash, size, err := provenance.HashFile(inputPath)
	if err != nil {
		return nil, err
	}
	an := &Analysis{InputPath: inputPath, InputHash: hash, InputSize: size}
	a := &assembler{
		psi:        ts.NewPSI(0),
		unwrap:     clock.NewUnwrapper(params.Clock),
		params:     params,
		current:    make(map[uint16]*Sample),
		snapAt:     make(map[string]*ts.Snapshot),
		maxSamples: params.IndexLimit + 1,
	}
	// Payload hashing needs the elementary bytes, so the pass keeps only the
	// packet headers and re-reads payloads from the offset list at close time.
	payloads, err := newPayloadReader(inputPath)
	if err != nil {
		return nil, err
	}
	defer payloads.Close()
	a.payloads = payloads

	for {
		p, ok, err := sc.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if err := a.feed(p); err != nil {
			return nil, err
		}
	}
	for _, cur := range a.current {
		a.close(cur)
	}
	// Recompute the payload hash of every sample from its cited packets.
	for i := range a.samples {
		h, err := payloads.SampleHash(a.samples[i].Offsets)
		if err != nil {
			return nil, err
		}
		a.samples[i].PayloadHash = h
	}
	an.Samples = a.samples
	an.Stats = sc.Stats()
	// Transport-level and PSI-level faults are both located diagnostics; a
	// PSI fault means a table was rejected, not that the run is unusable.
	an.Diagnostics = append(an.Diagnostics, sc.Diagnostics()...)
	an.Diagnostics = append(an.Diagnostics, a.psi.Diagnostics()...)
	sort.Slice(an.Diagnostics, func(i, j int) bool {
		if an.Diagnostics[i].Offset != an.Diagnostics[j].Offset {
			return an.Diagnostics[i].Offset < an.Diagnostics[j].Offset
		}
		return an.Diagnostics[i].Kind < an.Diagnostics[j].Kind
	})
	an.ClockErrors = a.unwrap.Finish()
	for _, s := range a.psi.Snapshots() {
		an.Snapshots = append(an.Snapshots, s)
	}
	if len(an.Snapshots) > 0 {
		for _, pr := range an.Snapshots[len(an.Snapshots)-1].Programs {
			for _, st := range pr.Streams {
				switch st.Kind {
				case ts.StreamVideo:
					if an.VideoPID == 0 {
						an.VideoPID = st.PID
					}
				case ts.StreamAudio:
					if an.AudioPID == 0 {
						an.AudioPID = st.PID
					}
				}
			}
		}
	}
	if a.limitHit {
		return an, &index.LimitsError{What: "samples", Limit: int64(params.IndexLimit), At: int64(len(a.samples))}
	}
	if len(an.Samples) == 0 {
		return an, errors.New("pipeline: no PSI-mapped elementary samples found")
	}
	firstV, firstOK := int64(0), false
	lastV := int64(0)
	for _, s := range an.Samples {
		if s.Kind != index.KindVideo {
			continue
		}
		if !firstOK {
			firstV = s.DTS
			firstOK = true
		}
		lastV = s.DTS
	}
	an.FirstVideoDTS = firstV
	an.LastVideoDTS = lastV
	for _, d := range an.Diagnostics {
		an.Errors = append(an.Errors, provenance.ErrorRecord{
			Stage: "ts", Kind: string(d.Kind), PID: int(d.PID), Offset: d.Offset,
			Packet: d.Packet, Detail: d.Detail, Blocking: false,
		})
	}
	for _, ce := range an.ClockErrors {
		an.Errors = append(an.Errors, provenance.ErrorRecord{
			Stage: "clock", Kind: string(ce.Kind), PID: int(ce.PID), Offset: ce.Offset,
			Packet: ce.PacketIndex, Detail: ce.Detail, Blocking: ce.Blocking,
		})
	}
	if params.RepairTimestamps {
		if blocked := a.unwrap.Errors().Blocking(); len(blocked) > 0 {
			return an, &BlockedError{Errors: blocked}
		}
	}
	return an, nil
}

// BlockedError carries the blocking timestamp conditions.
type BlockedError struct {
	Errors clock.ErrorList
}

// Error implements error.
func (b *BlockedError) Error() string {
	first := errors.New("none")
	if len(b.Errors) > 0 {
		first = b.Errors[0]
	}
	return fmt.Sprintf("pipeline: timestamp repair blocked by %d condition(s): %s", len(b.Errors), first.Error())
}

// Unwrap exposes the underlying conditions.
func (b *BlockedError) Unwrap() error { return b.Errors.Err() }

// payloadReader re-reads specific packets by offset and hashes their
// elementary payloads. Reads are sequential in practice (records are in
// source order), so an in-memory small cache avoids repeated syscalls.
type payloadReader struct {
	path  string
	mu    sync.Mutex
	cond  *sync.Cond
	f     *os.File
	cache map[int64][]byte
	order []int64
}

const payloadCacheEntries = 256

func newPayloadReader(path string) (*payloadReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &payloadReader{path: path, f: f, cache: make(map[int64][]byte)}, nil
}

// Close releases the file.
func (p *payloadReader) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.f == nil {
		return nil
	}
	err := p.f.Close()
	p.f = nil
	return err
}

// SampleHash hashes the elementary bytes of one access unit. The PES header
// is stripped from the first packet so the digest covers media bytes, not
// transport framing; continuation packets contribute their payload bytes.
func (p *payloadReader) SampleHash(offsets []int64) (string, error) {
	h := sha256.New()
	for i, off := range offsets {
		d, err := p.payloadAt(off)
		if err != nil {
			return "", err
		}
		if i == 0 {
			if stripped, ok := remux.StripPESHeader(d); ok {
				d = stripped
			}
		}
		h.Write(d)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p *payloadReader) payloadAt(off int64) ([]byte, error) {
	p.mu.Lock()
	if d, ok := p.cache[off]; ok {
		p.mu.Unlock()
		return d, nil
	}
	pkt, err := ts.ReadPacket(p.f, off)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	d := pkt.Payload
	p.cache[off] = d
	p.order = append(p.order, off)
	if len(p.order) > payloadCacheEntries {
		delete(p.cache, p.order[0])
		p.order = p.order[1:]
	}
	p.mu.Unlock()
	return d, nil
}

// ---- segmentation ----

// AssignSegments groups samples into output segments. Every segment opens on
// a video keyframe, lasts at least segmentTicks, and never straddles a PSI
// mapping change. Audio samples are bundled with the run of video they
// interleave, so one output carries a complete, playable span.
//
// The returned records are in source order and carry their segment number.
func AssignSegments(an *Analysis, segmentTicks int64) ([]index.Record, error) {
	// Split first: find the source index of every video keyframe that may open
	// an output. A keyframe opens one when it begins the stream, when the
	// accumulated span has reached segmentTicks, or when the PSI mapping
	// changed since the previous keyframe.
	var open []int
	lastOpen := -1
	lastSnap := ""
	for i := range an.Samples {
		s := &an.Samples[i]
		if s.Kind != index.KindVideo || !s.Keyframe {
			continue
		}
		if lastOpen < 0 {
			open = append(open, i)
			lastOpen = i
			lastSnap = s.SnapshotID
			continue
		}
		prev := an.Samples[lastOpen]
		span := s.DTS - prev.DTS
		changed := s.SnapshotID != lastSnap
		if span >= segmentTicks || changed {
			open = append(open, i)
			lastOpen = i
			lastSnap = s.SnapshotID
		}
	}
	if len(open) == 0 {
		return nil, errors.New("pipeline: no video keyframes to open a segment")
	}
	// A run ends right before the next opening keyframe; the final run ends
	// at end of stream. Audio that precedes the first opening keyframe is
	// carried into segment 0 so no samples are silently dropped.
	recs := make([]index.Record, 0, len(an.Samples))
	for k, start := range open {
		end := len(an.Samples)
		if k+1 < len(open) {
			end = open[k+1]
		}
		from := start
		if k == 0 {
			from = 0
			// Unowned audio before the first video sample still belongs to
			// this segment; video before the keyframe would not be decodable,
			// so it is not emitted.
			for from < start && an.Samples[from].Kind != index.KindAudio {
				from++
			}
		}
		for i := from; i < end; i++ {
			s := an.Samples[i]
			// Audio arriving after the last video frame of this run keeps its
			// segment, but video that belongs to the next run does not.
			recs = append(recs, recordOf(s, k, an))
		}
	}
	// Reject any segment whose first emitted video sample is not a keyframe:
	// that would make a random seek land on a non-decodable sample.
	if err := checkSegmentOpenings(recs); err != nil {
		return nil, err
	}
	return recs, nil
}

func recordOf(s Sample, seg int, an *Analysis) index.Record {
	return index.Record{
		PacketOffset:     s.Offsets[0],
		PacketIndex:      s.FirstPacket,
		SourceOffsets:    append([]int64(nil), s.Offsets...),
		PID:              s.PID,
		Kind:             s.Kind,
		DTS:              s.DTS,
		PTS:              s.PTS,
		Keyframe:         s.Keyframe,
		RandomAccessFlag: s.RandomAccess,
		PayloadBytes:     s.PayloadBytes,
		SnapshotID:       s.SnapshotID,
		Segment:          seg,
		PayloadHash:      s.PayloadHash,
	}
}

func checkSegmentOpenings(recs []index.Record) error {
	seenSeg := -1
	opened := false
	for _, r := range recs {
		if r.Segment != seenSeg {
			seenSeg = r.Segment
			opened = r.Keyframe && r.Kind == index.KindVideo
			if !opened {
				// The first sample of a segment may be audio that precedes
				// the keyframe in source order; the next video sample must
				// nevertheless be a keyframe.
				continue
			}
		}
		if r.Kind == index.KindVideo {
			if !opened && !r.Keyframe {
				return fmt.Errorf("pipeline: segment %d opens on a non-keyframe video sample (seq %d)",
					r.Segment, r.Seq)
			}
			opened = true
		}
	}
	return nil
}

// RunResult is the complete outcome of a pipeline run.
type RunResult struct {
	Analysis  *Analysis
	Index     *index.Index
	Segments  []remux.Segment
	Manifest  *provenance.Manifest
	Report    *dag.Report
	BlockedBy string
	Recovered int
	CacheHits int
}

// Options configures Run.
type Options struct {
	Workers       int
	CacheDir      string
	AllowRecovery bool
	MaxSegments   int
}

// Run executes the full pipeline. It returns a RunResult even when a node
// fails, so the caller reports the blocking node while the manifest still
// cites everything that was verified.
func Run(ctx context.Context, inputPath, outDir string, params Params, opts Options) (*RunResult, error) {
	if params.SegmentTicks <= 0 {
		params.SegmentTicks = DefaultParams().SegmentTicks
	}
	maxSeg := opts.MaxSegments
	if maxSeg <= 0 {
		maxSeg = params.MaxSegments
	}
	if maxSeg <= 0 {
		maxSeg = 32
	}
	res := &RunResult{}
	an, err := Analyze(ctx, inputPath, params)
	res.Analysis = an
	if err != nil {
		var blocked *BlockedError
		if errors.As(err, &blocked) {
			return finishBlocked(res, outDir, an, params, err)
		}
		return res, err
	}

	recs, err := AssignSegments(an, params.SegmentTicks)
	if err != nil {
		return res, err
	}
	segs := map[int]bool{}
	for _, r := range recs {
		segs[r.Segment] = true
	}
	if len(segs) > maxSeg {
		return res, fmt.Errorf("pipeline: %d segments exceed limit %d; split the input and rerun",
			len(segs), maxSeg)
	}

	bld := index.NewBuilder(params.IndexLimit)
	bld.SetTracks(an.VideoPID, an.AudioPID)
	for _, s := range an.Snapshots {
		bld.NoteSnapshot(s)
	}
	for _, r := range recs {
		if err := bld.Add(r); err != nil {
			return res, err
		}
	}
	idx, err := bld.Finish()
	if err != nil {
		return res, err
	}
	idxDigest := idx.Digest()
	res.Index = idx

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return res, err
	}
	man, err := provenance.New(inputPath, provenance.Params{
		Seed: params.Seed, SegmentTicks: params.SegmentTicks,
		TimeScale: clock.TimeScale, PCRPIDEcho: true, KeyframeOnly: params.KeyframeOnly,
		IndexLimit: params.IndexLimit, MaxFileBytes: params.MaxFileBytes,
		MaxPackets: params.MaxPackets, RepairTimestamps: params.RepairTimestamps,
	})
	if err != nil {
		return res, err
	}
	for _, e := range an.Errors {
		man.AddError(e)
	}
	res.Manifest = man

	segmentNumbers := make([]int, 0, len(segs))
	for n := range segs {
		segmentNumbers = append(segmentNumbers, n)
	}
	sort.Ints(segmentNumbers)

	w := remux.DefaultWriter()
	w.Version = provenance.ToolVersion
	var mu sync.Mutex
	segFacts := make(map[int]*remux.Segment)

	// The index-ready node pins the graph to one index revision.
	indexNode := dag.Node{
		Name: "index", Kind: "index",
		Params: map[string]string{
			"digest": idxDigest, "segments": fmt.Sprint(len(segs)),
			"vpid": fmt.Sprint(an.VideoPID), "apid": fmt.Sprint(an.AudioPID),
		},
		Inputs:        []string{an.InputHash},
		AllowRecovery: true,
		Run: func(ctx context.Context, key string, deps map[string]dag.Result) (dag.Result, error) {
			return dag.Result{Hash: idxDigest}, nil
		},
	}
	nodes := []dag.Node{indexNode}
	segNames := make([]string, 0, len(segmentNumbers))
	for _, n := range segmentNumbers {
		n := n
		name := fmt.Sprintf("segment-%02d", n)
		segNames = append(segNames, name)
		segRecs := recordsForSegment(recs, n)
		file := fmt.Sprintf("segment-%02d.mp4", n)
		nodes = append(nodes, dag.Node{
			Name: name, Kind: "remux",
			Params: map[string]string{
				"segment": fmt.Sprint(n), "index": idxDigest,
				"width": fmt.Sprint(w.Width), "height": fmt.Sprint(w.Height),
				"samples": fmt.Sprint(len(segRecs)),
			},
			Inputs:        []string{an.InputHash},
			DependsOn:     []string{"index"},
			AllowRecovery: true,
			Verify: func(ctx context.Context, key string, r dag.Result) error {
				sum, _, err := provenance.HashFile(filepath.Join(outDir, r.Detail))
				if err != nil {
					return err
				}
				if sum != r.Hash {
					return fmt.Errorf("remux: cached segment %s hash mismatch", r.Detail)
				}
				return nil
			},
			Run: func(ctx context.Context, key string, deps map[string]dag.Result) (dag.Result, error) {
				seg, err := w.WriteSegment(remux.SegmentSource{
					Number: n, Records: segRecs, IndexDigest: idxDigest, InputPath: an.InputPath,
				}, filepath.Join(outDir, file))
				if err != nil {
					return dag.Result{}, &dag.Failure{Node: name, Stage: "remux",
						Detail: err.Error(), Err: err, PID: -1, Offset: -1}
				}
				sum, size, err := provenance.HashFile(seg.Path)
				if err != nil {
					return dag.Result{}, err
				}
				if sum != seg.SHA256 || size != seg.Bytes {
					return dag.Result{}, &dag.Failure{Node: name, Stage: "verify",
						Detail: fmt.Sprintf("hash mismatch after write: disk=%s want=%s", sum, seg.SHA256),
						Err:    errors.New("segment hash mismatch"), PID: -1, Offset: -1}
				}
				mu.Lock()
				segFacts[n] = seg
				mu.Unlock()
				return dag.Result{Hash: sum, Detail: file}, nil
			},
		})
	}

	verifyNode := dag.Node{
		Name: "verify", Kind: "verify",
		Params:    map[string]string{"tool": provenance.ToolName, "version": provenance.ToolVersion},
		DependsOn: append([]string{"index"}, segNames...),
		Run: func(ctx context.Context, key string, deps map[string]dag.Result) (dag.Result, error) {
			for _, n := range segmentNumbers {
				r, ok := deps[fmt.Sprintf("segment-%02d", n)]
				if !ok || r.Hash == "" {
					return dag.Result{}, &dag.Failure{Node: "verify", Stage: "verify",
						Detail: fmt.Sprintf("missing segment %d", n), Err: errors.New("missing segment"),
						PID: -1, Offset: -1}
				}
			}
			return dag.Result{Hash: "verified"}, nil
		},
	}
	nodes = append(nodes, verifyNode)

	g, err := dag.Build(nodes)
	if err != nil {
		return res, err
	}
	runner := dag.Runner{Workers: opts.Workers, AllowRecovery: opts.AllowRecovery, Policy: dag.DefaultPolicy()}
	if opts.CacheDir != "" {
		dc, err := dag.NewDiskCache(opts.CacheDir, MaxCacheEntries)
		if err != nil {
			return res, err
		}
		runner.Cache = dc
	}
	report, runErr := runner.Run(ctx, g)
	res.Report = report
	res.BlockedBy = report.BlockedBy()
	res.Recovered = report.Recovered()
	if runErr != nil {
		return res, runErr
	}
	if f, blocked := report.Failed(); blocked {
		_ = f
		res.BlockedBy = report.BlockedBy()
		return res, runGraphError(report)
	}

	// Assemble the manifest in segment order. A recovered node does not run
	// its closure, so its facts are reconstructed from the index records and
	// the on-disk segment, which the Verify callback has already re-hashed.
	for _, n := range segmentNumbers {
		seg := segFacts[n]
		if seg == nil {
			var err error
			seg, err = recoveredFacts(n, filepath.Join(outDir, fmt.Sprintf("segment-%02d.mp4", n)),
				recordsForSegment(recs, n))
			if err != nil {
				return res, err
			}
		}
		res.Segments = append(res.Segments, *seg)
		if err := man.AddSegment(provenance.Segment{
			Number: seg.Number, File: filepath.Base(seg.Path), Size: seg.Bytes,
			SHA256: seg.SHA256, PayloadSHA: seg.PayloadHash,
			SampleCount: seg.SampleCount, VideoSamples: seg.VideoSamples,
			AudioSamples: seg.AudioSamples,
			StartPTS:     seg.VideoTiming.StartPTS, EndPTS: seg.VideoTiming.EndPTS,
			Keyframe:      seg.Keyframes > 0,
			SourceOffsets: seg.SourceOffsets, SourcePID: an.VideoPID,
			SnapshotIDs: seg.SnapshotIDs, IndexDigest: idxDigest,
		}); err != nil {
			return res, err
		}
	}
	for _, st := range report.States {
		if st.Status != dag.StatusDone && st.Status != dag.StatusRecovered {
			continue
		}
		man.AddDependency(provenance.Dependency{Node: st.Name, Key: st.Key, Hash: st.Result.Hash})
	}
	man.RecoveredNodes = report.Recovered()
	man.BlockedBy = report.BlockedBy()
	if err := man.Write(filepath.Join(outDir, "manifest.json")); err != nil {
		return res, err
	}
	return res, nil
}

// recoveredFacts rebuilds a segment's manifest facts without re-running the
// remuxer. Counts and timings come from the index records; the hash and size
// are read from the committed file, which Verify already validated.
func recoveredFacts(n int, path string, recs []index.Record) (*remux.Segment, error) {
	sum, size, err := provenance.HashFile(path)
	if err != nil {
		return nil, err
	}
	seg := &remux.Segment{Number: n, Path: path, Bytes: size, SHA256: sum}
	var v, a []remux.Sample
	for _, r := range recs {
		seg.SampleCount++
		seg.SourceOffsets = append(seg.SourceOffsets, r.SourceOffsets...)
		switch r.Kind {
		case index.KindVideo:
			seg.VideoSamples++
			if r.Keyframe {
				seg.Keyframes++
			}
			v = append(v, remux.Sample{Record: r, Keyframe: r.Keyframe})
		case index.KindAudio:
			seg.AudioSamples++
			a = append(a, remux.Sample{Record: r})
		}
		if !contains(seg.SnapshotIDs, r.SnapshotID) {
			seg.SnapshotIDs = append(seg.SnapshotIDs, r.SnapshotID)
		}
	}
	seg.VideoTiming = remux.TimingOf(v)
	seg.AudioTiming = remux.TimingOf(a)
	ph := ""
	for _, r := range recs {
		ph += r.PayloadHash
	}
	seg.PayloadHash = sumHash(ph)
	return seg, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func sumHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func runGraphError(rep *dag.Report) error {
	f, ok := rep.Failed()
	if !ok {
		return errors.New("pipeline: graph incomplete")
	}
	if f.Err != nil {
		return f.Err
	}
	return fmt.Errorf("pipeline: node %s blocked", f.Name)
}

func recordsForSegment(recs []index.Record, n int) []index.Record {
	var out []index.Record
	for _, r := range recs {
		if r.Segment == n {
			out = append(out, r)
		}
	}
	return out
}

func finishBlocked(res *RunResult, outDir string, an *Analysis, params Params, cause error) (*RunResult, error) {
	if an == nil {
		return res, cause
	}
	man, err := provenance.New(an.InputPath, provenance.Params{
		Seed: params.Seed, SegmentTicks: params.SegmentTicks, TimeScale: clock.TimeScale,
		IndexLimit: params.IndexLimit, MaxFileBytes: params.MaxFileBytes,
		MaxPackets: params.MaxPackets, RepairTimestamps: params.RepairTimestamps,
	})
	if err != nil {
		return res, err
	}
	for _, e := range an.Errors {
		man.AddError(e)
	}
	man.BlockedBy = "clock"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return res, err
	}
	if err := man.Write(filepath.Join(outDir, "manifest.json")); err != nil {
		return res, err
	}
	res.Manifest = man
	res.BlockedBy = "clock"
	return res, cause
}
