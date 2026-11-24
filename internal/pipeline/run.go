package pipeline

import (
	"os"

	"mediapipeline/internal/demux"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/probe"
	"mediapipeline/internal/remux"
	"mediapipeline/internal/track"
)

// Run processes inputPath into outputPath.  WorkDir holds temporary files and
// the checkpoint; it is created when missing.
func Run(workDir, inputPath, outputPath string, opts Options) (Summary, error) {
	sum := Summary{Input: inputPath, Output: outputPath}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return sum, err
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return sum, err
	}
	if opts.Resume {
		if resumed, ok := resumeIfCommitted(workDir, outputPath, data); ok {
			return resumed, nil
		}
	}

	res, err := probe.Detect(data)
	if err != nil {
		return fail(sum, err), err
	}
	sum.Format = string(res.Format)
	media, err := demuxInput(data, res)
	if err != nil {
		return fail(sum, err), err
	}
	sum.Tracks = len(media.Tracks)
	sum.EventsIn = len(media.Events)
	// Stage 1 result: the parsed model is committed to the work directory
	// before mapping starts, so an interruption leaves a usable checkpoint.
	if err := commitStageJSON(workDir, "parse", newParseRecord(media, sum)); err != nil {
		return fail(sum, err), err
	}
	sum.Cut = media.Cut
	for _, tr := range media.Tracks {
		sum.TrackIDs = append(sum.TrackIDs, tr.ID)
	}
	sum.PayloadSHA256In = MediaPayloadDigest(media)

	merged, err := metadata.Merge(media.Events)
	if err != nil {
		return fail(sum, fault.Wrap(fault.StageMetadata, sum.Format, fault.OpEvent, -1, "merge events", err)), err
	}
	sum.EventsOut = len(merged.Events)
	sum.Duplicates = merged.DuplicateCount
	sum.EventTimescale = merged.Timescale
	sum.EventOrderSHA256 = merged.Fingerprint()

	inputs := trackInputs(media.Tracks)
	mapping, err := opts.mapFor(inputs)
	if err != nil {
		return fail(sum, fault.Wrap(fault.StageMap, sum.Format, fault.OpMapping, -1, "build mapping", err)), err
	}
	if err := mapping.RequireComplete(inputs); err != nil {
		return fail(sum, fault.Wrap(fault.StageMap, sum.Format, fault.OpMapping, -1, "incomplete mapping", err)), err
	}
	if err := commitStageJSON(workDir, "map", mapping.Entries); err != nil {
		return fail(sum, err), err
	}

	cp := &Checkpoint{
		Stage:          StageMapping,
		Input:          inputPath,
		InputSHA256:    sha256Hex(data),
		Tracks:         mapping.Entries,
		EventTimescale: merged.Timescale,
	}
	for _, e := range merged.Events {
		cp.Events = append(cp.Events, EventRecord{
			Kind: string(e.Kind), TrackID: e.TrackID, Ticks: e.Time.Ticks,
			Timescale: e.Time.Timescale, Digest: e.DigestHex(),
		})
	}
	if err := writeCheckpoint(workDir, cp); err != nil {
		return fail(sum, err), err
	}

	tmp, err := stage(workDir, outputPath, StageRemux, func(tmp string) error {
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		out, err := remux.Write(f, remux.Plan{
			Src: data, Tracks: media.Tracks, Mapping: mapping,
			Events: merged, MajorBrand: media.MajorBrand, Cut: media.Cut,
		})
		if err != nil {
			return err
		}
		sum.OutputBytes = out.Bytes
		sum.PayloadBytes = out.PayloadBytes
		sum.PayloadSHA256Out = out.PayloadSHA256
		return nil
	})
	if err != nil {
		cp.Stage = StageRemux
		_ = writeCheckpoint(workDir, cp)
		return fail(sum, err), err
	}

	verr := VerifyFile(tmp, Verification{
		ExpectTracks:     len(media.Tracks),
		ExpectEvents:     len(merged.Events),
		PayloadSHA256:    sum.PayloadSHA256In,
		EventOrderSHA256: sum.EventOrderSHA256,
		Format:           sum.Format,
	})
	cp.Stage = StageValidate
	if verr == nil {
		_ = commitStageJSON(workDir, "validate", sum)
		if err := os.Rename(tmp, outputPath); err != nil {
			verr = fault.Wrap(fault.StageValidate, sum.Format, fault.OpWrite, -1, "commit output", err)
		} else {
			sum.Committed = true
			cp.OutputSHA256 = sum.PayloadSHA256Out
			cp.OutputBytes = sum.OutputBytes
		}
	}
	_ = writeCheckpoint(workDir, cp)
	if verr != nil {
		_ = os.Remove(tmp)
		_ = os.Remove(outputPath)
		sum.Committed = false
		return fail(sum, verr), verr
	}
	return sum, nil
}

// ParseOnly runs probe and demux and returns the media model without writing.
func ParseOnly(inputPath string) (*demux.Media, probe.Result, []byte, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, probe.Result{}, nil, err
	}
	res, err := probe.Detect(data)
	if err != nil {
		return nil, res, data, err
	}
	media, err := demuxInput(data, res)
	return media, res, data, err
}

func demuxInput(data []byte, res probe.Result) (*demux.Media, error) {
	switch res.Format {
	case probe.FormatMP4:
		return demux.ParseMP4(data, demux.Options{SampleEvents: true})
	case probe.FormatTS:
		return demux.ParseTS(data, demux.Options{})
	case probe.FormatRTP:
		return demux.ParseRTP(data)
	}
	return nil, fault.New(fault.StageProbe, string(res.Format), fault.OpUnknownFormat, 0, "unsupported format")
}

func trackInputs(tracks []demux.Track) []track.Input {
	out := make([]track.Input, len(tracks))
	for i, tr := range tracks {
		out[i] = track.Input{ID: tr.ID, Kind: tr.Kind, Timescale: tr.Timescale, Codec: tr.Codec}
	}
	return out
}
