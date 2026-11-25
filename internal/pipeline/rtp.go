// Package pipeline connects scanned indexes to recovery, mapping, and remux.
package pipeline

import (
	"mediacore/internal/clock"
	"mediacore/internal/fault"
	rtprecover "mediacore/internal/rtp"
	"mediacore/internal/scan"
)

const (
	PayloadVideo uint8 = 96
	PayloadAudio uint8 = 97
)

// RecoverRTP reads an indexed fixture, reorders/dedups packets, and builds
// lossless sample references whose Data points at recovered payloads.
func RecoverRTP(idx *scan.Index, window int) (*rtprecover.Report, []scan.SampleRef, error) {
	if idx == nil || idx.Format != scan.FormatRTP {
		return nil, nil, fault.New(fault.InvalidArgument, "RecoverRTP requires an RTP index")
	}
	packets, err := scan.ReadRTPFixturePackets(idx.SourcePath)
	if err != nil {
		return nil, nil, err
	}
	report, err := rtprecover.ReorderAll(packets, window)
	if err != nil {
		return nil, nil, err
	}

	metaBySSRC := map[uint32]scan.RTPStreamMeta{}
	for _, m := range idx.RTPStreams {
		metaBySSRC[m.SSRC] = m
	}
	for i := range report.Gaps {
		if m, ok := metaBySSRC[report.Gaps[i].SSRC]; ok {
			report.Gaps[i].TrackID = m.TrackID
		}
	}

	type tsInfo struct {
		extSeq uint64
		ts     uint32
	}
	lastTS := map[uint32]tsInfo{}
	indexByTrack := map[uint32]uint32{}
	samples := make([]scan.SampleRef, 0, len(report.Packets))
	for _, item := range report.Packets {
		p := item.Packet
		meta, ok := metaBySSRC[p.SSRC]
		if !ok {
			return nil, nil, fault.New(fault.UnsupportedTrack, "recovered packet has no SSRC metadata")
		}
		if p.PayloadType != PayloadVideo && p.PayloadType != PayloadAudio {
			return nil, nil, fault.New(fault.UnsupportedCodec, "unsupported RTP payload type")
		}
		if (meta.Kind == "video" && p.PayloadType != PayloadVideo) ||
			(meta.Kind == "audio" && p.PayloadType != PayloadAudio) {
			return nil, nil, fault.New(fault.UnsupportedCodec, "RTP payload type/kind mismatch")
		}

		units := int64(p.Timestamp)
		duration := defaultDuration(meta)
		if next, ok := lastTS[p.SSRC]; ok {
			_ = next
			// Duration is finalized by the following packet in a second pass
			// below; store a conservative default here.
		}
		tb := clock.Timebase{Timescale: meta.Timescale}
		idxInTrack := indexByTrack[meta.TrackID]
		indexByTrack[meta.TrackID] = idxInTrack + 1
		samples = append(samples, scan.SampleRef{
			TrackID:       meta.TrackID,
			Index:         idxInTrack,
			Size:          uint32(len(p.Payload)),
			Data:          append([]byte(nil), p.Payload...),
			PTSUnits:      units,
			DTSUnits:      units,
			DurationUnits: uint32(duration),
			PTSNS:         tb.ToNS(units),
			DTSNS:         tb.ToNS(units),
			KeyFrame:      true,
		})
		lastTS[p.SSRC] = tsInfo{extSeq: item.ExtSequence, ts: p.Timestamp}
	}
	fillRTPDurations(idx, samples)
	return report, samples, nil
}

func fillRTPDurations(idx *scan.Index, samples []scan.SampleRef) {
	tbByTrack := map[uint32]clock.Timebase{}
	metaByTrack := map[uint32]scan.RTPStreamMeta{}
	for _, m := range idx.RTPStreams {
		tbByTrack[m.TrackID] = clock.Timebase{Timescale: m.Timescale}
		metaByTrack[m.TrackID] = m
	}
	byTrack := map[uint32][]int{}
	for i, s := range samples {
		byTrack[s.TrackID] = append(byTrack[s.TrackID], i)
	}
	for track, inds := range byTrack {
		for j, i := range inds {
			if j+1 < len(inds) {
				cur := int64(samples[i].PTSUnits)
				next := int64(samples[inds[j+1]].PTSUnits)
				if d := next - cur; d > 0 && d < 1<<20 {
					samples[i].DurationUnits = uint32(d)
				}
			}
			tb := tbByTrack[track]
			samples[i].DurationNS = tb.ToNS(int64(samples[i].DurationUnits))
			samples[i].PTSNS = tb.ToNS(samples[i].PTSUnits)
			samples[i].DTSNS = samples[i].PTSNS
		}
	}
}

func defaultDuration(meta scan.RTPStreamMeta) int64 {
	switch meta.Codec {
	case "mcrv":
		if meta.Timescale == 90000 {
			return 3750
		}
		return int64(meta.Timescale / 24)
	case "mcau":
		return 960
	default:
		return 1
	}
}
