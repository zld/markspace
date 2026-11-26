package main

import (
	"path/filepath"

	"replaykit/internal/fixture"
	"replaykit/media/audio"
	"replaykit/media/audit"
	"replaykit/media/clock"
	"replaykit/media/fault"
	gomp4mp4 "replaykit/media/mp4"
	"replaykit/media/recovery"
	"replaykit/media/rtp"
	"replaykit/media/segment"
)

func caseDir(root, name string) string { return filepath.Join(root, name) }

func normalHigh() []byte     { return fixture.NormalHigh() }
func normalLow() []byte      { return fixture.NormalLow() }
func truncatedVideo() []byte { return fixture.TruncatedVideo(30) }
func lossDupStream() fixture.AudioStream {
	return fixture.LossDup()
}
func tamperedSidecar(idx *gomp4mp4.Index) ([]byte, *gomp4mp4.Sidecar, error) {
	return fixture.TamperedSidecar(idx)
}

// indexOnly scans and builds a sidecar-free index for digest comparison.
func indexOnly(data []byte) (*gomp4mp4.Index, error) {
	limits := gomp4mp4.DefaultLimits()
	scan, err := gomp4mp4.ScanBytes(data, limits)
	if err != nil {
		return nil, err
	}
	return gomp4mp4.BuildIndex(data, scan, limits, nil)
}

// proofEvents maps the generated events.json DTOs onto segment events.
func proofEvents() []segment.Event {
	return eventsFromEF(fixture.ProofEventFile())
}

func eventsFromEF(ef fixture.EventFile) []segment.Event {
	out := make([]segment.Event, 0, len(ef.Events))
	for _, e := range ef.Events {
		pre, post := e.PreNS, e.PostNS
		if pre == 0 {
			pre = ef.DefaultPreNS
		}
		if post == 0 {
			post = ef.DefaultPostNS
		}
		out = append(out, segment.Event{ID: e.ID, AtNS: e.AtNS, PreNS: pre, PostNS: post})
	}
	return out
}

// newLayer scans, indexes and clocks one rendition. A non-nil sidecar is only
// trusted inside BuildIndex after its checksum verifies.
func newLayer(name string, data []byte, frameTicks int64, side *gomp4mp4.Sidecar) (*segment.Layer, error) {
	limits := gomp4mp4.DefaultLimits()
	scan, err := gomp4mp4.ScanBytes(data, limits)
	if err != nil {
		return nil, err
	}
	idx, err := gomp4mp4.BuildIndex(data, scan, limits, side)
	if err != nil {
		return nil, err
	}
	// The parser is layer-agnostic; stamp every collected anomaly with the
	// bitrate rendition name so the manifest attributes it to a layer.
	for i := range idx.Anomalies {
		if idx.Anomalies[i].Layer == "" {
			idx.Anomalies[i].Layer = name
		}
	}
	clk, err := clock.New(idx.Track.Timescale, frameTicks)
	if err != nil {
		return nil, err
	}
	return &segment.Layer{
		Name: name, Data: data, Scan: scan, Index: idx, Clock: clk,
		InputDigest: audit.HashBytes(data),
	}, nil
}

// clockNS represents a nanosecond interval endpoint as a canonical integer
// tick so a fatal, timescale-less failure can still report its event interval.
func clockNS(ns int64) clock.Tick { return clock.Tick{Value: ns, RemDen: 1} }

// toItems turns recoverable windows into checkpointed recovery items.
func toItems(ws []*segment.Window) []recovery.Item {
	items := make([]recovery.Item, 0, len(ws))
	for _, w := range ws {
		if !w.Recoverable || len(w.Bytes) == 0 {
			continue
		}
		rec := w.Record()
		items = append(items, recovery.Item{
			Key: rec.Key, File: w.FileName(), InputDigest: w.InputDigest,
			RangeStartNS: w.StartNS, RangeEndNS: w.EndNS,
			MediaStart: rec.MediaStart, MediaEnd: rec.MediaEnd,
			Hash: w.Digest, Bytes: w.Bytes,
		})
	}
	return items
}

type audioResult struct {
	stream      fixture.AudioStream
	check       rtp.Check
	parseAnom   []fault.Anomaly
	durationOK  int
	durationBad int
	durationTot int
	durationAn  []fault.Anomaly
}

func (a audioResult) anomalies() []fault.Anomaly {
	out := append([]fault.Anomaly{}, a.parseAnom...)
	out = append(out, a.check.Anomalies...)
	out = append(out, a.durationAn...)
	return out
}

// runAudio parses, reorders and durations one framed RTP stream.
func runAudio(st fixture.AudioStream) audioResult {
	pkts, parseAnom, _ := rtp.ParseFramed(st.Framed, 1<<16)
	check := rtp.Order(pkts, rtp.DefaultOptions(st.Params.SampleRate))
	res := audioResult{stream: st, check: check, parseAnom: parseAnom}
	for _, p := range check.Packets {
		res.durationTot++
		if _, err := audio.PacketDuration(st.Params, p.Payload); err != nil {
			res.durationBad++
			res.durationAn = append(res.durationAn,
				audio.RecordError("audio", p.Offset, p.Timestamp, err))
		} else {
			res.durationOK++
		}
	}
	return res
}

func countCategory(as []fault.Anomaly, c fault.Category) int {
	n := 0
	for _, a := range as {
		if a.Category == c {
			n++
		}
	}
	return n
}

func pct(ok, total int) int {
	if total == 0 {
		return 0
	}
	return 100 * ok / total
}
