// Package remux writes lossless MP4/MOV output from indexed samples.
package remux

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"mediacore/internal/fault"
	"mediacore/internal/scan"
)

// ErrInterrupt is returned by Options.AfterSample to simulate a clean stop at
// a complete sample boundary.
var ErrInterrupt = errors.New("simulated interrupt after complete sample")

// Options controls resumable remux output.
type Options struct {
	OutputPath     string
	AllowedRoot    string
	Resume         bool
	AfterSample    func(committed int) error
	CheckpointPath string
}

// Result describes a completed remux.
type Result struct {
	OutputPath     string `json:"output_path"`
	Bytes          int64  `json:"bytes"`
	SampleCount    int    `json:"sample_count"`
	Resumed        bool   `json:"resumed"`
	OutputSHA256   string `json:"output_sha256,omitempty"`
	CheckpointPath string `json:"checkpoint_path,omitempty"`
}

type plannedSample struct {
	ref     scan.SampleRef
	track   scan.Track
	mapping scan.TrackMapping
	offset  uint32
	data    []byte
}

// WriteMP4 losslessly copies indexed payload into a new MP4 container.
func WriteMP4(idx *scan.Index, mappings []scan.TrackMapping, samples []scan.SampleRef, opt Options) (*Result, error) {
	if idx == nil {
		return nil, fault.New(fault.InvalidArgument, "nil index")
	}
	if err := ensureAllowed(opt.AllowedRoot, opt.OutputPath); err != nil {
		return nil, err
	}
	if len(mappings) == 0 {
		mappings = idx.Mapping
	}
	plan, total, planHash, err := buildPlan(idx, mappings, samples)
	if err != nil {
		return nil, err
	}

	outPath := opt.OutputPath
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), outPath, -1)
	}
	tempPath := outPath + ".partial"
	cpPath := opt.CheckpointPath
	if cpPath == "" {
		cpPath = outPath + ".checkpoint.json"
	}

	cp, resumed, err := loadValidCheckpoint(cpPath, idx.SourceSHA256, outPath, tempPath, planHash)
	if err != nil {
		return nil, err
	}
	if !opt.Resume {
		_ = os.Remove(cpPath)
		_ = os.Remove(tempPath)
		cp = nil
		resumed = false
	}

	var f *os.File
	var committed int
	var committedBytes int64
	if resumed && cp != nil {
		f, err = os.OpenFile(tempPath, os.O_RDWR, 0o644)
		if err != nil {
			return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, -1)
		}
		want := cp.DataStart + cp.CommittedBytes
		if st, err := f.Stat(); err != nil || st.Size() < want {
			f.Close()
			_ = os.Remove(cpPath)
			_ = os.Remove(tempPath)
			return WriteMP4(idx, mappings, samples, opt.withResume(false))
		}
		if err := f.Truncate(want); err != nil {
			f.Close()
			return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, want)
		}
		if _, err := f.Seek(want, io.SeekStart); err != nil {
			f.Close()
			return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, want)
		}
		committed = cp.Committed
		committedBytes = cp.CommittedBytes
	} else {
		_ = os.Remove(cpPath)
		_ = os.Remove(tempPath)
		f, err = os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
		if err != nil {
			return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, -1)
		}
		mw := mp4.NewWriter(f)
		if err := writeFtyp(mw); err != nil {
			f.Close()
			return nil, err
		}
		const dataStart = int64(32) // 24-byte ftyp + 8-byte mdat header
		if _, err := mp4.WriteBoxInfo(f, &mp4.BoxInfo{Type: mp4.BoxTypeMdat(), Size: uint64(8 + total)}); err != nil {
			f.Close()
			return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, 24)
		}
		cp = &checkpoint{
			Version: 1, SourceSHA256: idx.SourceSHA256, OutputPath: outPath, TempPath: tempPath,
			DataStart: dataStart, TotalPayload: total, PlanSHA256: planHash,
		}
	}

	var src *os.File
	defer func() {
		if src != nil {
			src.Close()
		}
	}()

	for i := committed; i < len(plan); i++ {
		ps := plan[i]
		data := ps.ref.Data
		if data == nil {
			if src == nil {
				src, err = os.Open(idx.SourcePath)
				if err != nil {
					f.Close()
					return nil, fault.At(fault.Wrap(fault.IOFailure, err), idx.SourcePath, -1)
				}
			}
			data = make([]byte, ps.ref.Size)
			if _, err := src.ReadAt(data, ps.ref.Offset); err != nil {
				f.Close()
				return nil, fault.At(fault.Wrap(fault.TruncatedData, err), idx.SourcePath, ps.ref.Offset)
			}
		}
		if uint32(len(data)) != ps.ref.Size {
			f.Close()
			return nil, fault.New(fault.Verification, "copied sample size mismatch")
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, int64(ps.offset))
		}
		committed++
		committedBytes += int64(len(data))
		cp.Committed = committed
		cp.CommittedBytes = committedBytes
		if err := saveCheckpoint(cpPath, cp); err != nil {
			f.Close()
			return nil, err
		}
		if opt.AfterSample != nil {
			if err := opt.AfterSample(committed); err != nil {
				_ = f.Sync()
				f.Close()
				if errors.Is(err, ErrInterrupt) {
					return &Result{CheckpointPath: cpPath, Resumed: resumed}, ErrInterrupt
				}
				return nil, err
			}
		}
	}

	mdatEnd := cp.DataStart + cp.TotalPayload
	if err := f.Truncate(mdatEnd); err != nil {
		f.Close()
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, mdatEnd)
	}
	if _, err := f.Seek(mdatEnd, io.SeekStart); err != nil {
		f.Close()
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, mdatEnd)
	}
	cfgs := buildTrackConfigs(idx, mappings, plan)
	if err := writeMoov(f, cfgs, idx); err != nil {
		f.Close()
		return nil, err
	}
	finalSize, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		f.Close()
		return nil, fault.Wrap(fault.IOFailure, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, -1)
	}
	if err := f.Close(); err != nil {
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), tempPath, -1)
	}

	// The completed temporary container must itself be scannable before it is
	// allowed to replace the final output.
	rescan, err := scan.BuildIndex(tempPath, idx.Params)
	if err != nil {
		return nil, err
	}
	if len(rescan.Samples) != len(plan) {
		return nil, fault.New(fault.Verification, "remux rescan sample count mismatch")
	}
	if err := os.Rename(tempPath, outPath); err != nil {
		return nil, fault.At(fault.Wrap(fault.IOFailure, err), outPath, -1)
	}
	_ = os.Remove(cpPath)
	digest, _ := scan.SHA256File(outPath)
	return &Result{OutputPath: outPath, Bytes: finalSize, SampleCount: len(plan), Resumed: resumed || committed > 0 && opt.Resume, OutputSHA256: digest}, nil
}

func (o Options) withResume(v bool) Options { o.Resume = v; return o }
