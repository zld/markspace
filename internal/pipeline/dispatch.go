package pipeline

import (
	"os"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/model"
	"example.com/mediavault/internal/packageindex"
)

// Ingest dispatches one input through its subsystem.
func (r *Runner) Ingest(in Input) Report {
	switch in.Kind {
	case "", "wav":
		return r.ingestWAV(in)
	case "rtp":
		return r.ingestRTP(in)
	case "mp4":
		return r.inspectMP4(in)
	default:
		return r.reject(Report{Name: in.Name}, model.StageReceived,
			fault.New(fault.CodeArgs, fault.StageReceived, -1, "unknown input kind %q", in.Kind))
	}
}

// inspectMP4 indexes an existing archive wrapper. It never creates a new PCM
// asset: a valid wrapper is confirmed readable, a corrupt one is rejected so
// that an already-archived product is reported, not silently replaced.
func (r *Runner) inspectMP4(in Input) Report {
	rep := Report{Name: in.Name}
	if _, err := os.Stat(in.Path); err != nil {
		return r.reject(rep, model.StageReceived, fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "open %s", in.Path))
	}
	r.transition(&rep, in.Name, "", model.StageReceived)
	idx, err := packageindex.Scan(in.Path)
	if err != nil {
		return r.reject(rep, model.StageScanned, err)
	}
	r.transition(&rep, in.Name, model.StageReceived, model.StageScanned)
	f, err := os.Open(in.Path)
	if err != nil {
		return r.reject(rep, model.StageScanned, fault.Wrap(fault.CodeIO, fault.StageScanned, -1, err, "open %s", in.Path))
	}
	err = packageindex.ValidateChunkOffsets(f, idx)
	f.Close()
	if err != nil {
		return r.reject(rep, model.StageScanned, err)
	}
	r.transition(&rep, in.Name, model.StageScanned, model.StageArchived)
	rep.Asset = model.Asset{Name: in.Name, Path: in.Path, Transport: "mp4", Stage: model.StageArchived}
	rep.ArchiveDigest = idx.ContainerDigest
	rep.Reused = len(idx.Boxes)
	return r.appendReport(rep)
}
