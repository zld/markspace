package pipeline

import (
	"bytes"
	"os"
	"path/filepath"

	"liveregress/internal/fault"
	"liveregress/internal/fmp4"
	"liveregress/internal/remux"
	"liveregress/internal/ts"
	"liveregress/internal/verify"
)

// remuxTSFile copies every access unit of a transport-stream segment into an
// fMP4 segment without touching the bytes, then re-parses the output and
// compares payload digests. Transport streams scatter an access unit across
// packets, so each unit's bytes are supplied directly to the remux writer.
func (r *Runner) remuxTSFile(layer, segPath string) (*SegmentReport, error) {
	out := &SegmentReport{Layer: layer, Path: segPath}
	abs := filepath.Join(r.opts.Root, filepath.FromSlash(segPath))
	raw, err := os.ReadFile(abs)
	if err != nil {
		return out, fault.Wrap(fault.KindPath, segPath, -1, "read transport stream", err)
	}
	if int64(len(raw)) > r.opts.MaxSegmentBytes {
		return out, fault.New(fault.KindLimit, segPath, 0,
			"transport stream exceeds the per-segment limit").Recover(true)
	}
	out.InputSHA256 = verify.HashBytes(raw)
	d, derr := ts.DemuxFile(bytes.NewReader(raw), segPath)
	if d == nil || len(d.Tracks) == 0 {
		if derr != nil {
			return out, derr
		}
		return out, fault.New(fault.KindMissing, segPath, 0, "no media tracks in transport stream")
	}
	tr := d.Tracks[0]
	if tr.Kind != "video" {
		return out, fault.New(fault.KindUnsupported, segPath, 0,
			"only H.264/H.265 transport streams are copied").Recover(false)
	}
	if len(tr.Samples) == 0 {
		return out, fault.New(fault.KindTruncate, segPath, 0,
			"transport stream yielded no complete access units").Recover(true)
	}
	plan := remux.Track{InputID: tr.TrackID, OutputID: 1, Timescale: tr.Timescale,
		Handler: "vide", Codec: "avc1", Width: 640, Height: 360}
	var payload []byte
	var decode uint64
	for _, s := range tr.Samples {
		plan.Samples = append(plan.Samples, remux.Sample{
			TrackID: 1, SourceOffset: s.AUOffset, Size: uint32(len(s.AUBytes)),
			Duration: 3000, Keyframe: s.Keyframe, Time: decode,
			Payload: append([]byte(nil), s.AUBytes...),
		})
		payload = append(payload, s.AUBytes...)
		decode += 3000
	}

	outPath := filepath.Join(r.outRoot, layer, trimM4S(segPath)+".mp4")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return out, fault.Wrap(fault.KindPath, outPath, -1, "create output dir", err)
	}
	w, err := remux.NewWriter(outPath, segPath, bytes.NewReader(nil), remux.Options{MaxTotalBytes: r.opts.MaxSegmentBytes})
	if err != nil {
		return out, err
	}
	if err := w.WriteInit([]remux.Track{plan}); err != nil {
		w.Close()
		return out, err
	}
	if _, err := w.WriteFragment(remux.FragmentPlan{Sequence: 1, TrackID: 1, Samples: plan.Samples}); err != nil {
		w.Close()
		return out, err
	}
	if err := w.Close(); err != nil {
		return out, fault.Wrap(fault.KindPath, outPath, -1, "close ts remux", err)
	}
	outData, err := os.ReadFile(outPath)
	if err != nil {
		return out, fault.Wrap(fault.KindPath, outPath, -1, "read ts remux", err)
	}
	out.FileSHA256 = verify.HashBytes(outData)
	out.Bytes = w.Bytes()
	out.Samples = int64(len(plan.Samples))
	out.Keyframes = countKeyframes(plan.Samples)

	parsed, perr := fmp4.ParseSegmentBytes(outData, outPath)
	if perr != nil {
		return out, perr
	}
	var got []byte
	for _, f := range parsed.Fragments {
		for _, s := range f.Samples {
			got = append(got, outData[s.Offset:s.Offset+int64(s.Size)]...)
		}
	}
	out.PayloadSHA256 = verify.HashBytes(got)
	out.SourcePayloadSHA256 = verify.HashBytes(payload)
	if !bytes.Equal(got, payload) || out.PayloadSHA256 != out.SourcePayloadSHA256 {
		return out, fault.New(fault.KindVerify, outPath, 0,
			"transport-stream remux changed access unit bytes").Recover(false)
	}
	out.Complete = true
	return out, nil
}
