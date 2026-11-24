package batch

import (
	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/maptrack"
	"mediapack/internal/probe"
)

type loadedCatalog struct {
	catalog       maptrack.Catalog
	errorRanges   []demux.ErrorRange
	unrecoverable []*fault.Error
}

func loadInputs(cfg Config) loadedCatalog {
	var lc loadedCatalog
	for _, path := range cfg.MP4Inputs {
		info, err := probe.Detect(path)
		if err != nil {
			fe := fault.Wrap(fault.KindUnrecoverable, err)
			lc.unrecoverable = append(lc.unrecoverable, fe)
			lc.errorRanges = append(lc.errorRanges, faultToRange(fe))
			continue
		}
		_ = info
		doc, err := demux.DemuxMP4(path)
		if doc != nil {
			lc.catalog.MP4 = append(lc.catalog.MP4, doc)
			lc.errorRanges = append(lc.errorRanges, doc.ErrorRanges()...)
		}
		if err != nil {
			fe := fault.Wrap(fault.KindUnrecoverable, err)
			lc.unrecoverable = append(lc.unrecoverable, fe)
			if doc == nil {
				lc.errorRanges = append(lc.errorRanges, faultToRange(fe))
			}
		}
	}
	rate := cfg.RTPClockRate
	if rate == 0 {
		rate = 48000
	}
	for _, path := range cfg.RTPInputs {
		if _, err := probe.Detect(path); err != nil {
			fe := fault.Wrap(fault.KindUnrecoverable, err)
			lc.unrecoverable = append(lc.unrecoverable, fe)
			lc.errorRanges = append(lc.errorRanges, faultToRange(fe))
			continue
		}
		doc, err := demux.DemuxRTP(path, rate)
		if doc != nil {
			lc.catalog.RTP = append(lc.catalog.RTP, doc)
		}
		if err != nil {
			fe := fault.Wrap(fault.KindRTP, err)
			if fe.Kind == "" {
				fe.Kind = fault.KindRTP
			}
			lc.unrecoverable = append(lc.unrecoverable, fe)
			lc.errorRanges = append(lc.errorRanges, faultToRange(fe))
		}
	}
	return lc
}

func faultToRange(e *fault.Error) demux.ErrorRange {
	if e == nil {
		return demux.ErrorRange{}
	}
	return demux.ErrorRange{
		Path: e.Path, Kind: string(e.Kind), TrackID: e.TrackID,
		StartOffset: e.Offset, EndOffset: e.Offset,
		Recoverable: e.Recoverable, Message: e.Error(),
	}
}
