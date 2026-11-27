package replay

import (
	"os"
	"path/filepath"
	"sort"

	"replay/internal/clock"
	"replay/internal/fault"
	"replay/internal/graph"
	"replay/internal/ingest"
	"replay/internal/mapasset"
	"replay/internal/scenario"
	"replay/internal/state"
)

// runIngest parses RTP and RTCP, persisting only counts, hashes and
// deficits. The recovery position counts datagrams processed so a resume
// can report the exact ingestion frontier.
func (e *Engine) runIngest() (outputHash string, recovery int, unit string, files []string, err error) {
	parsers := map[string]*ingest.Parser{}
	for _, track := range e.Fixture.Tracks {
		p, perr := ingest.NewParser(track.ID, track.ClockRate)
		if perr != nil {
			return "", 0, "", nil, stageError(StageIngest, perr)
		}
		parsers[track.ID] = p
	}

	art := &IngestArtifact{ReportsByTrack: map[string]bool{}}
	processed := 0
	for _, datagram := range e.Fixture.RTP {
		parser, ok := parsers[datagram.Track]
		if !ok {
			return "", 0, "", nil, fault.New(fault.KindInput, "rtp for unknown track").
				At(StageIngest).WithTrack(datagram.Track).WithOffset(datagram.Offset)
		}
		if aerr := parser.Add(datagram.Bytes, datagram.Offset); aerr != nil {
			return "", 0, "", nil, stageError(StageIngest, aerr)
		}
		processed++
	}
	var streams []ingest.Stream
	for _, track := range e.Fixture.Tracks {
		streams = append(streams, parsers[track.ID].Finish()...)
	}
	sort.SliceStable(streams, func(i, j int) bool {
		if streams[i].Track != streams[j].Track {
			return streams[i].Track < streams[j].Track
		}
		return streams[i].SSRC < streams[j].SSRC
	})
	art.Streams = streams

	for i, datagram := range e.Fixture.RTCP {
		data := datagram.Bytes
		if datagram.Truncate > 0 {
			if datagram.Truncate >= len(data) {
				return "", 0, "", nil, fault.New(fault.KindTruncated, "rtcp truncation exceeds datagram").
					At(StageIngest).WithTrack(datagram.Track).
					WithOffset(datagram.Offset).WithIndex(i)
			}
			data = data[:len(data)-datagram.Truncate]
		}
		report, rerr := ingest.ParseRTCPWithPion(datagram.Track, data, datagram.Offset, i)
		if rerr != nil {
			return "", 0, "", nil, stageError(StageIngest, rerr)
		}
		art.Reports = append(art.Reports, report)
		art.ReportsByTrack[datagram.Track] = true
	}
	art.PacketsProcessed = processed

	path := e.path(fileIngest)
	if werr := writeJSON(path, art); werr != nil {
		return "", 0, "", nil, fault.New(fault.KindInput, "cannot write ingest artefact").
			At(StageIngest).Wrap(werr)
	}
	digest, _, herr := fileDigest(path)
	if herr != nil {
		return "", 0, "", nil, stageError(StageIngest, herr)
	}
	e.ingestArt = art
	return digest, processed, "packets", []string{fileIngest}, nil
}

// runTimeline anchors every track clock and maps each accepted packet to
// the unified nanosecond timeline. Tracks without an RTCP report are
// anchored on their first valid packet and marked estimated.
func (e *Engine) runTimeline() (string, int, string, []string, error) {
	if e.ingestArt == nil {
		if err := e.loadArtifacts(); err != nil {
			return "", 0, "", nil, stageError(StageTimeline, err)
		}
	}
	trackOrder := e.trackOrder()
	art := &TimelineArtifact{Sources: map[string]string{}}
	points := 0

	for _, track := range trackOrder {
		stream := findStream(e.ingestArt.Streams, track.ID)
		if stream == nil {
			return "", 0, "", nil, fault.New(fault.KindInput, "track has no ingested stream").
				At(StageTimeline).WithTrack(track.ID)
		}
		clk, cerr := clock.New(track.ID, track.ClockRate)
		if cerr != nil {
			return "", 0, "", nil, stageError(StageTimeline, cerr)
		}

		report := findReport(e.ingestArt.Reports, track.ID)
		if report != nil {
			wallNS, nerr := clock.NTPToNanoseconds(report.NTP)
			if nerr != nil {
				return "", 0, "", nil, stageError(StageTimeline, nerr)
			}
			if aerr := clk.AnchorRTCP(report.RTPTime, wallNS); aerr != nil {
				return "", 0, "", nil, stageError(StageTimeline, aerr)
			}
		} else if len(stream.Packets) > 0 {
			if aerr := clk.AnchorFirstPacket(stream.Packets[0].Timestamp, 0); aerr != nil {
				return "", 0, "", nil, stageError(StageTimeline, aerr)
			}
			art.Estimated = append(art.Estimated, track.ID)
		} else {
			return "", 0, "", nil, fault.New(fault.KindInput, "track has no valid packets to anchor clock").
				At(StageTimeline).WithTrack(track.ID)
		}

		art.Sources[track.ID] = string(clk.Source())
		for _, pkt := range stream.Packets {
			point, merr := clk.Map(clock.Tick{Timestamp: pkt.Timestamp, Seq: pkt.Extended})
			if merr != nil {
				return "", 0, "", nil, stageError(StageTimeline, merr)
			}
			art.Points = append(art.Points, point)
			points++
		}
		art.Wraps += stream.Wraps
	}

	clock.SortPoints(art.Points)
	path := e.path(fileTimeline)
	if err := writeJSON(path, art); err != nil {
		return "", 0, "", nil, fault.New(fault.KindInput, "cannot write timeline artefact").
			At(StageTimeline).Wrap(err)
	}
	digest, _, err := fileDigest(path)
	if err != nil {
		return "", 0, "", nil, stageError(StageTimeline, err)
	}
	e.timelineArt = art
	return digest, points, "timeline-points", []string{fileTimeline}, nil
}

// runGraph builds and validates the dependency graph. A cycle fails the
// stage before any mapping or merge artefact exists.
func (e *Engine) runGraph() (string, int, string, []string, error) {
	g, err := graph.New(e.Fixture.Tracks)
	if err != nil {
		return "", 0, "", nil, stageError(StageGraph, err)
	}
	order, err := g.TopologicalOrder()
	if err != nil {
		return "", 0, "", nil, stageError(StageGraph, err)
	}
	art := &GraphArtifact{Order: order}
	for _, id := range order {
		t, _ := g.Track(id)
		art.Tracks = append(art.Tracks, t)
	}
	path := e.path(fileGraph)
	if err := writeJSON(path, art); err != nil {
		return "", 0, "", nil, fault.New(fault.KindInput, "cannot write graph artefact").
			At(StageGraph).Wrap(err)
	}
	digest, _, err := fileDigest(path)
	if err != nil {
		return "", 0, "", nil, stageError(StageGraph, err)
	}
	e.graphArt = art
	return digest, len(order), "tracks", []string{fileGraph}, nil
}

// runMap writes one MP4 per asset spec and maps versions onto intervals.
func (e *Engine) runMap() (string, int, string, []string, error) {
	if e.graphArt == nil || e.ingestArt == nil || e.timelineArt == nil {
		if err := e.loadArtifacts(); err != nil {
			return "", 0, "", nil, stageError(StageMap, err)
		}
	}
	assetDir := filepath.Join(e.WorkDir, "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		return "", 0, "", nil, fault.New(fault.KindInput, "cannot create asset directory").
			At(StageMap).Wrap(err)
	}

	art := &MapArtifact{}
	written := 0
	for _, track := range e.graphArt.Order {
		var assets []mapasset.Asset
		var records []AssetRecord
		for _, spec := range e.Fixture.Assets {
			if spec.Track != track {
				continue
			}
			data, berr := scenario.AssetBytes(spec)
			if berr != nil {
				return "", 0, "", nil, fault.New(fault.KindInput, "cannot build asset").
					At(StageMap).WithVersion(spec.Version).WithTrack(track).Wrap(berr)
			}
			name := filepath.ToSlash(filepath.Join("assets", spec.Track+"-"+spec.Version+".mp4"))
			full := filepath.Join(e.WorkDir, filepath.FromSlash(name))
			if werr := os.WriteFile(full, data, 0o644); werr != nil {
				return "", 0, "", nil, fault.New(fault.KindInput, "cannot write asset").
					At(StageMap).WithTrack(track).Wrap(werr)
			}
			asset, perr := mapasset.ProbeFile(spec.Version, spec.Track, full, spec.TrackOffsetNS)
			if perr != nil {
				return "", 0, "", nil, stageError(StageMap, perr)
			}
			assets = append(assets, asset)
			records = append(records, AssetRecord{
				Version: spec.Version,
				Track:   spec.Track,
				Path:    name,
				Digest:  asset.Digest,
				StartNS: asset.Interval.Start,
				EndNS:   asset.Interval.End,
			})
			written++
		}

		want := mapasset.Interval{Start: e.Fixture.WantInterval[0], End: e.Fixture.WantInterval[1]}
		res := mapasset.Map(track, assets, want)
		// Report versions that supply no asset for this track at all.
		present := map[string]bool{}
		for _, a := range assets {
			present[a.Version] = true
		}
		for _, version := range e.Fixture.Versions {
			if !present[version] {
				res.Missing = append(res.Missing, mapasset.MissingVersion(version, track, want))
			}
		}
		art.Tracks = append(art.Tracks, TrackMap{Track: track, Result: res, Assets: records})
		art.Missing += len(res.Missing)
		art.Conflicts += len(res.Conflicts)
		art.Merged += res.Merged
	}

	path := e.path(fileMap)
	if err := writeJSON(path, art); err != nil {
		return "", 0, "", nil, fault.New(fault.KindInput, "cannot write map artefact").
			At(StageMap).Wrap(err)
	}
	digest, _, err := fileDigest(path)
	if err != nil {
		return "", 0, "", nil, stageError(StageMap, err)
	}
	e.mapArt = art
	return digest, written, "assets", []string{fileMap}, nil
}

func findStream(streams []ingest.Stream, track string) *ingest.Stream {
	for i := range streams {
		if streams[i].Track == track {
			return &streams[i]
		}
	}
	return nil
}

func findReport(reports []ingest.ClockReport, track string) *ingest.ClockReport {
	for i := range reports {
		if reports[i].Track == track {
			return &reports[i]
		}
	}
	return nil
}

// trackOrder returns fixture tracks in declared order.
func (e *Engine) trackOrder() []graph.Track {
	out := append([]graph.Track(nil), e.Fixture.Tracks...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func fileDigest(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fault.New(fault.KindInput, "cannot read artefact").
			At("replay").Wrap(err)
	}
	return state.HashBytes(data), int64(len(data)), nil
}
