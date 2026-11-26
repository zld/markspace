package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"example.com/archiveverify/internal/cmaf"
	"example.com/archiveverify/internal/model"
	rtptrace "example.com/archiveverify/internal/rtp"
	"example.com/archiveverify/internal/timebase"
)

// BuildOptions controls deterministic index construction.
type BuildOptions struct {
	Root             string
	InputFingerprint string
	RTPWindow        int
	MaxBox           int64
}

// Build parses all manifest events in order and constructs the index.
func Build(ctx context.Context, m *cmaf.Manifest, opts BuildOptions) (*BuildResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	if opts.MaxBox <= 0 {
		opts.MaxBox = cmaf.MaxBoxSize
	}
	root := opts.Root
	initPath := resolveInput(root, m.InitPath)
	init, err := cmaf.ParseInitFile(initPath)
	if err != nil {
		return nil, err
	}

	idx := &Index{Version: 1, Case: m.Case, InputFingerprint: opts.InputFingerprint}
	mapper := timebase.NewMapper()
	var reports []*timebase.Report
	var anchorResults []timebase.SyncResult
	var clocks []timebase.ClockAnchor
	fileSeen := map[string]bool{}
	addFile := func(rel string) error {
		if rel == "" || fileSeen[rel] {
			return nil
		}
		rec, err := hashFile(root, rel)
		if err != nil {
			return err
		}
		idx.Files = append(idx.Files, rec)
		fileSeen[rel] = true
		return nil
	}
	if err := addFile(m.InitPath); err != nil {
		return nil, err
	}

	for _, ev := range m.Events {
		if err := ctx.Err(); err != nil {
			return nil, context.Cause(ctx)
		}
		switch ev.Kind {
		case cmaf.EventInit:
			if ev.Path != "" {
				if err := addFile(ev.Path); err != nil {
					return nil, err
				}
			}
		case cmaf.EventFragment:
			if err := addFile(ev.Path); err != nil {
				return nil, err
			}
			full := resolveInput(root, ev.Path)
			frag, perr := cmaf.ParseFragmentFile(full, ev.SourceID, init)
			if perr != nil {
				var me *model.MediaError
				if errors.As(perr, &me) && me.Code == model.CodeParse {
					idx.Quarantine = append(idx.Quarantine, model.Quarantine{
						Source: ev.SourceID, File: ev.Path, BoxType: me.BoxType,
						BoxOffset: me.BoxOffset, FirstUnreadable: me.FirstUnreadable,
						Reason: me.Error(),
					})
					continue
				}
				return nil, perr
			}
			rep, merr := mapper.MapFragment(ev.SourceID, ev.Index, frag)
			if merr != nil {
				return nil, merr
			}
			reports = append(reports, rep)
			for _, s := range rep.Samples {
				role := ""
				if t := init.Track(s.TrackID); t != nil {
					role = t.Handler
				}
				idx.Entries = append(idx.Entries, Entry{
					TrackID: s.TrackID, Role: role, SourceID: s.SourceID,
					EventIndex: s.EventIndex, Sequence: s.Sequence, SampleIndex: s.SampleIndex,
					StartUs: s.StartUs, EndUs: s.EndUs, Timescale: s.Timescale,
					RawStart: s.RawStart, RawEnd: s.RawEnd, KeyFrame: s.KeyFrame,
					File: ev.Path, BoxOffset: s.BoxOffset, ByteOffset: s.ByteOffset, Size: s.Size,
				})
				if s.KeyFrame && role == "vide" {
					idx.KeyFrames = append(idx.KeyFrames, KeyFrame{
						TrackID: s.TrackID, StartUs: s.StartUs, File: ev.Path,
						Offset: s.BoxOffset, Sequence: s.Sequence,
					})
				}
			}
		case cmaf.EventAnchor:
			if ev.Anchor != nil {
				clock, sync, aerr := timebase.AnchorFromSpec(*ev.Anchor)
				if aerr != nil {
					return nil, aerr
				}
				clocks = append(clocks, clock)
				anchorResults = append(anchorResults, sync)
			}
		}
	}

	tl := mapper.AggregateReport(reports...)
	idx.TimeMinUs, idx.TimeMaxUs = tl.TimeMin, tl.TimeMax
	idx.MFHDGaps = append(idx.MFHDGaps, mapper.Gaps...)
	idx.Repairs = append(idx.Repairs, mapper.Repairs...)

	arrivals, err := rtptrace.ArrivalsFromSpecs(m.RTPTrace)
	if err != nil {
		return nil, err
	}
	rtpReport := rtptrace.RecoverTrace(arrivals, rtptrace.Config{Window: opts.RTPWindow})
	var rtpTimeline []timebase.MappedRTPPoint
	if len(clocks) > 0 {
		points := make([]timebase.RTPPointInput, 0, len(rtpReport.Ordered))
		for _, p := range rtpReport.Ordered {
			points = append(points, timebase.RTPPointInput{
				Sequence: p.SequenceNumber, Timestamp: p.Timestamp,
			})
		}
		rtpTimeline = clocks[0].MapRTPPoints(points)
	}
	idx.InputFingerprint = opts.InputFingerprint
	return &BuildResult{
		Index: idx, Timeline: tl, RTP: rtpReport, RTPTimeline: rtpTimeline,
		Anchors: anchorResults,
	}, nil
}

func resolveInput(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, filepath.FromSlash(p))
}

func hashFile(root, rel string) (FileRecord, error) {
	full := resolveInput(root, rel)
	st, err := os.Stat(full)
	if err != nil {
		return FileRecord{}, model.NewParseError(rel, "", -1, -1, err)
	}
	f, err := os.Open(full)
	if err != nil {
		return FileRecord{}, model.NewParseError(rel, "", 0, 0, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, cmaf.MaxBoxSize+1)); err != nil {
		return FileRecord{}, err
	}
	return FileRecord{Path: filepath.ToSlash(rel), Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
