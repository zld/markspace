package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"liveregress/internal/fault"
	"liveregress/internal/fmp4"
	"liveregress/internal/manifest"
	"liveregress/internal/remux"
	"liveregress/internal/verify"
)

// segmentOutcome is the result of processing one segment file.
type segmentOutcome struct {
	report     SegmentReport
	keyframes  []keyframeFact
	samples    []sampleFact
	isolate    bool
	layerFault error
}

// sampleFact locates one sample in its source segment so a rebuild can read
// the bytes without trusting container keyframe flags.
type sampleFact struct {
	Time     int64
	Offset   int64
	Size     uint32
	TrackID  uint32
	Keyframe bool
}

// keyframeFact is a keyframe actually present in the media.
type keyframeFact struct {
	Time    int64
	Offset  int64
	Size    uint32
	TrackID uint32
}

// processSegment parses one segment, remuxes its complete samples, and proves
// the output payload equals the source sample bytes.
func (r *Runner) processSegment(layer, kind string, seg manifest.Segment) segmentOutcome {
	out := segmentOutcome{}
	out.report = SegmentReport{Layer: layer, Path: seg.Path}

	inputData, err := os.ReadFile(filepath.Join(r.opts.Root, filepath.FromSlash(seg.Path)))
	if err != nil {
		out.report.Faults = append(out.report.Faults, mustFault(err))
		out.isolate = true
		out.layerFault = fault.Wrap(fault.KindPath, seg.Path, -1, "read segment", err)
		return out
	}
	if int64(len(inputData)) > r.opts.MaxSegmentBytes {
		fe := fault.New(fault.KindLimit, seg.Path, 0, "segment exceeds the per-segment limit").Recover(true)
		out.report.Faults = append(out.report.Faults, fe)
		return out
	}
	out.report.InputSHA256 = verify.HashBytes(inputData)

	// A segment whose input digest changed since the checkpoint was written is
	// rescanned from scratch; a segment with an unchanged digest is parsed and
	// verified again anyway, so a resumed run reports exactly what a cold run
	// does. The checkpoint is what proves the digest continuity in the report.
	if _, ok := r.cp.Done0(layer, seg.Sequence); ok {
		if _, reuse := r.cp.Reuse(layer, seg.Sequence, out.report.InputSHA256); !reuse && r.opts.Recover {
			out.report.Rescanned = true
		}
	}

	initBytes, err := r.loadInit(seg)
	if err != nil {
		return r.faultSegment(out, err, true)
	}
	init, err := fmp4.ParseInitBytes(initBytes, seg.Path+"#init")
	if err != nil {
		return r.faultSegment(out, err, true)
	}
	if len(init.Tracks) != 1 {
		return r.faultSegment(out, fault.New(fault.KindMapping, seg.Path, -1,
			fmt.Sprintf("expected exactly one track in init, found %d", len(init.Tracks))), true)
	}
	tr := init.Tracks[0]
	if err := fmp4.CodecSupport(tr.Codec, tr.Channels, tr.SampleSize, tr.SampleRate); err != nil {
		if fe, ok := fault.As(err); ok {
			fe.Path = seg.Path
		}
		return r.faultSegment(out, err, true)
	}

	parsed, perr := fmp4.ParseSegmentBytes(inputData, seg.Path)
	if perr != nil {
		fe, isFault := fault.As(perr)
		if !isFault {
			fe = mustFault(perr)
			isFault = true
		}
		if !fe.Recoverable {
			// Structural damage stops this representation only. It is
			// recoverable at run scope because every other layer can still be
			// published, so the exit code reflects a partial success.
			if isolatingKind(fe.Kind) {
				fe.Recover(true)
			}
			out.report.Faults = append(out.report.Faults, fe)
			out.isolate = true
			out.layerFault = perr
			return out
		}
		// A recoverable parse fault means a truncated tail or an internal
		// defect that stops only this representation: keep the complete
		// prefix of fragments and submit only what parsed.
		out.report.Faults = append(out.report.Faults, fe)
		if isolatingKind(fe.Kind) {
			out.isolate = true
			out.layerFault = perr
		}
	}
	if parsed == nil || len(parsed.Fragments) == 0 {
		if len(out.report.Faults) > 0 {
			// The parse already explained why nothing usable was found; do not
			// stack a second, severity-inflating fault on top of it.
			out.isolate = true
			out.layerFault = out.report.Faults[len(out.report.Faults)-1]
			return out
		}
		return r.faultSegment(out, fault.New(fault.KindMissing, seg.Path, -1,
			"no complete fragment in segment"), false)
	}

	var samples []fmp4.Sample
	for _, f := range parsed.Fragments {
		if f.TrackID != tr.TrackID {
			ferr := fault.New(fault.KindMapping, seg.Path, f.MoofOffset,
				fmt.Sprintf("fragment track %d does not match init track %d", f.TrackID, tr.TrackID)).
				WithTrack(f.TrackID, "")
			return r.faultSegment(out, ferr, true)
		}
		samples = append(samples, f.Samples...)
	}
	if len(samples) == 0 {
		return r.faultSegment(out, fault.New(fault.KindTruncate, seg.Path, 0,
			"segment yielded no complete samples").Recover(true), false)
	}

	outSamples := make([]remux.Sample, 0, len(samples))
	for _, s := range samples {
		out.samples = append(out.samples, sampleFact{Time: int64(s.DecodeTime), Offset: s.Offset,
			Size: s.Size, TrackID: s.TrackID, Keyframe: s.Keyframe})
		if int64(s.Size) > r.opts.MaxSegmentBytes {
			fe := fault.New(fault.KindLimit, seg.Path, s.Offset,
				fmt.Sprintf("sample size %d exceeds limit", s.Size)).Recover(true)
			out.report.Faults = append(out.report.Faults, fe)
			break
		}
		outSamples = append(outSamples, remux.Sample{
			TrackID: tr.TrackID, SourceOffset: s.Offset, Size: s.Size,
			Duration: s.Duration, CompositionOffset: s.CompositionOffset,
			Keyframe: s.Keyframe, Time: s.DecodeTime,
		})
		if s.Keyframe {
			out.keyframes = append(out.keyframes, keyframeFact{Time: int64(s.DecodeTime),
				Offset: s.Offset, Size: s.Size, TrackID: s.TrackID})
		}
	}

	trackPlan := remux.Track{InputID: tr.TrackID, OutputID: tr.TrackID, Timescale: tr.Timescale,
		Handler: handlerFor(kind), Codec: tr.Codec, Channels: tr.Channels,
		SampleSize: tr.SampleSize, SampleRate: tr.SampleRate, Width: tr.Width,
		Height: tr.Height, Language: trLanguage(tr.Language)}
	outPath := filepath.Join(r.outRoot, layer, filepath.Base(seg.Path))
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return r.faultSegment(out, fault.Wrap(fault.KindPath, outPath, -1, "create output dir", err), true)
	}
	src, err := os.Open(filepath.Join(r.opts.Root, filepath.FromSlash(seg.Path)))
	if err != nil {
		return r.faultSegment(out, fault.Wrap(fault.KindPath, seg.Path, -1, "open source", err), true)
	}
	defer src.Close()
	w, err := remux.NewWriter(outPath, seg.Path, src, remux.Options{MaxTotalBytes: r.opts.MaxSegmentBytes})
	if err != nil {
		return r.faultSegment(out, err, true)
	}
	if err := w.WriteInit([]remux.Track{trackPlan}); err != nil {
		w.Close()
		return r.faultSegment(out, err, true)
	}
	// Write one moof+mdat per source fragment so fragment boundaries are
	// preserved exactly.
	var written []remux.Sample
	offset := 0
	for fi, f := range parsed.Fragments {
		fragSamples := outSamples[offset : offset+len(f.Samples)]
		offset += len(f.Samples)
		if _, err := w.WriteFragment(remux.FragmentPlan{Sequence: f.Sequence, TrackID: tr.TrackID,
			BaseDecodeTime: f.BaseDecodeTime, Samples: fragSamples}); err != nil {
			w.Close()
			fe, _ := fault.As(err)
			if fe != nil && fe.Recoverable {
				out.report.Faults = append(out.report.Faults, fe)
				break
			}
			return r.faultSegment(out, err, true)
		}
		written = append(written, fragSamples...)
		_ = fi
	}
	if err := w.Close(); err != nil {
		return r.faultSegment(out, fault.Wrap(fault.KindPath, outPath, -1, "close output", err), true)
	}
	if len(written) == 0 {
		return out
	}
	out.report.Bytes = w.Bytes()
	out.report.Samples = int64(len(written))
	out.report.Complete = len(written) == len(samples) && len(out.report.Faults) == 0
	out.report.Keyframes = countKeyframes(written)

	outData, err := os.ReadFile(outPath)
	if err != nil {
		return r.faultSegment(out, fault.Wrap(fault.KindPath, outPath, -1, "read output", err), true)
	}
	out.report.FileSHA256 = verify.HashBytes(outData)

	sourceDigest, err := hashSourceRanges(src, written)
	if err != nil {
		return r.faultSegment(out, err, true)
	}
	// Content identity: the remuxed mdat payload must hash the same as the
	// exact source sample byte ranges. A mismatch leaves SourceRangeSHA256
	// empty so the segment never counts as a SHA-256 match.
	payloadDigest, err := hashOutputPayload(outData, outPath, len(written))
	if err != nil {
		return r.faultSegment(out, err, true)
	}
	if payloadDigest == sourceDigest {
		out.report.PayloadSHA256 = payloadDigest
		out.report.SourcePayloadSHA256 = sourceDigest
	} else {
		fe := fault.New(fault.KindVerify, seg.Path, 0,
			fmt.Sprintf("remuxed payload digest %s != source digest %s", payloadDigest, sourceDigest)).
			Recover(false)
		out.report.Faults = append(out.report.Faults, fe)
		out.report.Complete = false
	}
	if out.report.Complete {
		if err := r.cp.Commit(recoverState(layer, seg, out.report)); err != nil {
			r.report.AddFault(err)
		}
	}
	return out
}

// isolatingKind reports whether a defect stops its representation. Tail
// truncation and resource limits keep their complete prefix and let the layer
// continue; structural damage stops only the affected layer.
func isolatingKind(k fault.Kind) bool {
	switch k {
	case fault.KindFormat, fault.KindCorrupt, fault.KindMapping,
		fault.KindUnsupported, fault.KindMissing, fault.KindVerify:
		return true
	}
	return false
}

func (r *Runner) faultSegment(out segmentOutcome, err error, isolate bool) segmentOutcome {
	fe := mustFault(err)
	// A layer-scoped defect is recoverable at run scope: the run can still
	// publish every unaffected representation.
	if isolate && isolatingKind(fe.Kind) {
		fe.Recover(true)
	}
	out.report.Faults = append(out.report.Faults, fe)
	if isolate && isolatingKind(fe.Kind) {
		out.isolate = true
		out.layerFault = err
	}
	return out
}

func (r *Runner) loadInit(seg manifest.Segment) ([]byte, error) {
	if seg.Init == nil {
		return nil, fault.New(fault.KindMissing, seg.Path, -1,
			"segment has no EXT-X-MAP init reference").Recover(false)
	}
	data, err := os.ReadFile(filepath.Join(r.opts.Root, filepath.FromSlash(seg.Init.Path)))
	if err != nil {
		return nil, fault.Wrap(fault.KindPath, seg.Init.Path, -1, "read init", err)
	}
	if br := seg.Init.ByteRange; br != nil {
		if br.Offset < 0 || br.Offset+br.Length > int64(len(data)) {
			return nil, fault.New(fault.KindTruncate, seg.Init.Path, br.Offset,
				"init byte range past end of file").Recover(false)
		}
		return data[br.Offset : br.Offset+br.Length], nil
	}
	return data, nil
}

func hashSourceRanges(src *os.File, samples []remux.Sample) (string, error) {
	ranges := make([]verify.Range, 0, len(samples))
	for _, s := range samples {
		ranges = append(ranges, verify.Range{Offset: s.SourceOffset, Length: int64(s.Size)})
	}
	digest, _, err := verify.NewRangeHasher(src).HashRanges(ranges)
	if err != nil {
		return "", fault.Wrap(fault.KindCorrupt, src.Name(), -1, "hash source ranges", err)
	}
	return digest, nil
}

// hashOutputPayload hashes the mdat payload of each remuxed fragment in order.
func hashOutputPayload(outData []byte, outPath string, wantSamples int) (string, error) {
	seg, err := fmp4.ParseSegmentBytes(outData, outPath)
	if err != nil {
		return "", err
	}
	var ranges []verify.Range
	total := 0
	for _, f := range seg.Fragments {
		for _, s := range f.Samples {
			ranges = append(ranges, verify.Range{Offset: s.Offset, Length: int64(s.Size)})
			total++
		}
	}
	if total != wantSamples {
		return "", fault.New(fault.KindVerify, outPath, -1,
			fmt.Sprintf("remuxed segment has %d samples, wrote %d", total, wantSamples)).Recover(false)
	}
	f, err := os.Open(outPath)
	if err != nil {
		return "", fault.Wrap(fault.KindPath, outPath, -1, "open output for hashing", err)
	}
	defer f.Close()
	digest, _, err := verify.NewRangeHasher(f).HashRanges(ranges)
	if err != nil {
		return "", fault.Wrap(fault.KindCorrupt, outPath, -1, "hash output payload", err)
	}
	return digest, nil
}

func countKeyframes(samples []remux.Sample) int {
	n := 0
	for _, s := range samples {
		if s.Keyframe {
			n++
		}
	}
	return n
}

func handlerFor(kind string) string {
	if kind == "video" {
		return "vide"
	}
	return "soun"
}

func trLanguage(tag string) [3]byte {
	var out [3]byte
	if len(tag) == 3 {
		copy(out[:], tag)
		return out
	}
	copy(out[:], "und")
	return out
}

func mustFault(err error) *fault.Error {
	if fe, ok := fault.As(err); ok {
		return fe
	}
	return fault.Wrap(fault.KindFormat, "", -1, err.Error(), err)
}
