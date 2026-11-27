// Input assembly helpers for the continuity run.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"continuity/internal/cerr"
	"continuity/internal/clock"
	"continuity/internal/fixture"
	"continuity/internal/manifest"
	"continuity/internal/matroska"
	"continuity/internal/media"
	"continuity/internal/merge"
	"continuity/internal/rtp"
	"continuity/internal/syncer"
)

func buildTracks(seg *matroska.Segment, colr media.Colour) ([]media.TrackParams, map[uint64]string, error) {
	var tracks []media.TrackParams
	kindOf := map[uint64]string{3: "audio"}
	for _, t := range seg.Tracks {
		tp := media.TrackParams{Number: t.Number, Source: "matroska"}
		switch t.Type {
		case 1:
			tp.Kind = "video"
			if t.Colour.Present {
				name, ok := media.ColorName(t.Colour.Matrix)
				if !ok {
					name = fmt.Sprintf("reserved(%d)", t.Colour.Matrix)
				}
				tp.ColorSpace = name
			}
		case 2:
			tp.Kind = "audio"
			tp.Channels = int(t.Channels)
			tp.SampleRate = int(t.SampleRate)
		default:
			return nil, nil, cerr.New(cerr.CodeTypeUnsupported, -1,
				fmt.Sprintf("track[%d]", t.Number), "unsupported track type")
		}
		kindOf[t.Number] = tp.Kind
		tracks = append(tracks, tp)
	}
	// Backup-source colour metadata from the MP4 init segment.
	name, ok := media.ColorName(uint64(colr.Matrix))
	if !ok {
		name = fmt.Sprintf("reserved(%d)", colr.Matrix)
	}
	tracks = append(tracks, media.TrackParams{Number: 1, Kind: "video", Source: "mp4-init", ColorSpace: name})
	// Backup-source audio from the RTP stream.
	tracks = append(tracks, media.TrackParams{Number: 3, Kind: "audio", Source: "rtp",
		Channels: 2, SampleRate: fixture.RTPClockRate})
	return tracks, kindOf, nil
}

func buildEvents(seg *matroska.Segment, committed []rtp.Committed, kindOf map[uint64]string) ([]rawEvent, int64) {
	var raws []rawEvent
	trackIdx := map[uint64]uint64{}
	maxTs := int64(0)
	for _, b := range seg.Blocks {
		idx := trackIdx[b.TrackNumber]
		trackIdx[b.TrackNumber]++
		raws = append(raws, rawEvent{ev: merge.Event{
			TimestampNs: b.TimestampNs, Track: b.TrackNumber, Index: idx,
			Kind: kindOf[b.TrackNumber], DurationNs: b.DurationNs,
		}, payload: b.Payload})
		if b.TimestampNs > maxTs {
			maxTs = b.TimestampNs
		}
	}
	// The backup RTP source continues the program timeline.
	rtpBase := maxTs + 20_000_000
	for _, c := range committed {
		ts, err := clock.ToNanosAtRate(int64(c.Packet.Timestamp-fixture.RTPBaseTS), fixture.RTPClockRate)
		if err != nil {
			continue // fixture timestamps cannot overflow
		}
		raws = append(raws, rawEvent{ev: merge.Event{
			TimestampNs: rtpBase + ts, Track: 3, Index: c.Ext - fixture.RTPBaseSeq,
			Kind: "audio", DurationNs: 20_000_000,
		}, payload: c.Packet.Payload})
	}
	return raws, rtpBase
}

func buildManifest(opts Options, norm []media.TrackParams, decisions []media.Decision,
	st rtp.Stats, sum syncer.Summary, rtpBase int64, in fixture.Inputs,
	events int, digest uint64) *manifest.Manifest {
	m := &manifest.Manifest{
		Seed:        opts.Seed,
		Tracks:      norm,
		Decisions:   decisions,
		Events:      events,
		EventDigest: fmt.Sprintf("%016x", digest),
		Sync:        manifest.SyncSummary{Pairs: sum.Pairs, MaxDeviationNs: sum.MaxDeviationNs},
		RTP: manifest.RTPStats{
			Received: st.Received, Lost: st.Lost, Duplicated: st.Duplicated,
			Late: st.Late, Missing: st.Missing,
		},
	}
	for _, rc := range st.Recoveries {
		ts, _ := clock.ToNanosAtRate(int64(rc.RTPTimestamp-fixture.RTPBaseTS), fixture.RTPClockRate)
		m.Recovery = append(m.Recovery, manifest.Recovery{Seq: rc.Seq, TimestampNs: rtpBase + ts})
	}
	mk := sha256.Sum256(in.Matroska)
	mp := sha256.Sum256(in.MP4Init)
	rh := sha256.New()
	for _, p := range in.RTP {
		rh.Write(p)
	}
	m.Input = manifest.InputDigest{
		MatroskaSHA256: hex.EncodeToString(mk[:]), MatroskaBytes: len(in.Matroska),
		MP4SHA256: hex.EncodeToString(mp[:]), MP4Bytes: len(in.MP4Init),
		RTPSHA256: hex.EncodeToString(rh.Sum(nil)), RTPPackets: len(in.RTP),
	}
	return m
}
