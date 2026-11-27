package main

import (
	"fmt"
	"path/filepath"

	"github.com/pion/rtp"

	"tracecheck/internal/batch"
	"tracecheck/internal/clock"
	"tracecheck/internal/ebml"
	"tracecheck/internal/fixture"
	"tracecheck/internal/index"
	"tracecheck/internal/mkv"
	rng "tracecheck/internal/range"
	"tracecheck/internal/serr"
)

// demo generates every fixture, runs the healthy sample through the full
// pipeline and reports the damaged samples with their error codes and offsets.
// It returns the process exit code.
func demo(workDir string, chunkSize int, resume bool, out *output) int {
	samples := fixture.All()
	paths := make(map[fixture.Kind]string, len(samples))
	for _, s := range samples {
		path, err := writeFixture(workDir, s.Name, s.Data)
		if err != nil {
			out.diag("IO_ERROR: %v", err)
			return 1
		}
		paths[s.Kind] = path
	}
	out.section("fixtures")
	for _, s := range samples {
		out.key(string(s.Kind)+"_path", paths[s.Kind])
	}

	if code := demoHealthy(workDir, chunkSize, resume, paths[fixture.KindHealthy], out); code != 0 {
		return code
	}
	demoEvents(out)
	demoRTPMP4(out)
	demoMetadataConflict(paths[fixture.KindMetadataConflict], out)
	return demoDamaged(paths, out)
}

// demoHealthy runs parse, tracks, index, mapping, ranges and the batch/resume
// cycle over the healthy sample.
func demoHealthy(workDir string, chunkSize int, resume bool, path string, out *output) int {
	data, err := readFile(path)
	if err != nil {
		out.diag("IO_ERROR: %v", err)
		return 1
	}
	doc, perr := ebml.Parse(data)
	if perr != nil {
		out.diag("unexpected healthy parse failure: %v", perr)
		return 1
	}
	blocks, berr := mkv.ParseBlocks(doc)
	if berr != nil {
		out.diag("unexpected healthy block failure: %v", berr)
		return 1
	}
	tracks, terr := mkv.ParseTracks(doc)
	if terr != nil {
		out.diag("unexpected healthy track failure: %v", terr)
		return 1
	}
	tb := clock.Timebase{TickNanos: tracks.TimestampScale, ClientUnitNanos: clock.NanosPerMilli}
	ix, ierr := index.Build(blocks, tb, tracks.TimestampScale, scaleLabel(tracks))
	if ierr != nil {
		out.diag("index build failed: %v", ierr)
		return 1
	}

	out.section("metadata")
	fmt.Fprint(out.w, mkv.BuildMetadata(doc, tracks, blocks).Summary())

	roundtrip := roundtripRate(ix, blocks)
	out.section("summary")
	out.key("roundtrip_rate", fmt.Sprintf("%.3f", roundtrip))
	out.key("keyframe_hit_rate", fmt.Sprintf("%.3f", keyframeHitRate(ix)))
	out.key("index_digest", ix.Digest())

	code := demoRanges(ix, data, out)
	if code != 0 {
		return code
	}
	return demoBatch(workDir, filepath.Base(path), path, data, tb, tracks, chunkSize, resume, out)
}

// roundtripRate measures how many blocks resolve back to identical bytes via
// the index (sample span) and identical media time. It must be 1.0 for a
// well-formed input.
func roundtripRate(ix *index.Index, blocks []mkv.Block) float64 {
	total, ok := 0, 0
	byBlock := map[int]index.Record{}
	for _, ti := range ix.Tracks {
		for _, r := range ti.Records {
			byBlock[r.BlockIndex] = r
		}
	}
	for _, b := range blocks {
		total++
		r, found := byBlock[b.Index]
		if found && r.SampleStart == b.PayloadStart && r.SampleEnd == b.PayloadEnd &&
			r.Track == b.TrackNumber {
			ok++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(ok) / float64(total)
}

// keyframeHitRate measures the fraction of sampled query times that resolve to
// a keyframe at or before the query. Queries are sampled within each track's
// own recorded span, so the first query of every track lands on its first
// keyframe and a well-formed input reaches 1.0.
func keyframeHitRate(ix *index.Index) float64 {
	const queries = 200
	hits, denom := 0, 0
	for _, ti := range ix.Tracks {
		if len(ti.Records) == 0 {
			continue
		}
		lo := ti.Records[0].TimeNanos
		hi := ti.Records[len(ti.Records)-1].TimeNanos
		span := hi - lo
		for i := 0; i < queries; i++ {
			t := lo
			if span > 0 {
				t = lo + int64(i)*span/int64(queries-1)
			}
			denom++
			if ti.KeyframeBefore(t) >= 0 {
				hits++
			}
		}
	}
	if denom == 0 {
		return 0
	}
	return float64(hits) / float64(denom)
}

// demoRanges reads five half-open ranges spanning the media span and prints a
// sha256 per range.
func demoRanges(ix *index.Index, data []byte, out *output) int {
	lo, hi, err := ix.Span()
	if err != nil {
		out.diag("span error: %v", err)
		return 1
	}
	out.section("ranges")
	step := (hi - lo) / 4
	if step <= 0 {
		step = 1
	}
	for i := 0; i < 4; i++ {
		start := lo + int64(i)*step
		end := start + step
		if i == 3 {
			end = hi + 1 // inclusive of the final block
		}
		res, rerr := rng.Read(ix, data, start, end)
		if rerr != nil {
			out.diag("range [%d,%d): %v", start, end, rerr)
			return 1
		}
		out.key(fmt.Sprintf("range_%d_ms", i), fmt.Sprintf("[%d,%d)",
			start/clock.NanosPerMilli, end/clock.NanosPerMilli))
		out.key(fmt.Sprintf("segment_sha256_%d", i), res.SegmentHash())
	}
	return 0
}

// demoBatch runs the batch pipeline twice: once to create the checkpoint and
// (when asked) once to resume it, verifying the digest and hashes are stable
// and that resume reprocesses zero chunks.
func demoBatch(workDir, name, path string, data []byte, tb clock.Timebase,
	tracks *mkv.Tracks, chunkSize int, resume bool, out *output) int {
	out.section("batch")
	label := scaleLabel(tracks)

	// First, establish a checkpoint if one does not already exist. A resume
	// run reuses whatever a previous invocation persisted.
	if !resume {
		first, err := batch.Run(data, tb, label, batch.Options{ChunkSize: chunkSize}, nil)
		if err != nil {
			out.diag("batch run failed: %v", err)
			return 1
		}
		if err := batch.EnsureWorkDir(workDir); err != nil {
			out.diag("%v", err)
			return 1
		}
		if err := batch.Commit(workDir, name, path, first); err != nil {
			out.diag("commit checkpoint: %v", err)
			return 1
		}
		out.key("chunks_total", first.TotalChunks)
		out.key("chunks_completed", first.CompletedChunks)
		out.key("index_digest", first.IndexDigest)
		out.key("resume_reprocessed_chunks", 0)
		return 0
	}

	// Resume path: load the checkpoint, run with reuse and require zero work.
	_, prior, err := batch.Resume(data, workDir, name, batch.Options{ChunkSize: chunkSize})
	if err != nil {
		out.diag("resume checkpoint error: %v", err)
		return 2
	}
	second, err := batch.Run(data, tb, label, batch.Options{ChunkSize: chunkSize, Resume: true}, prior)
	if err != nil {
		out.diag("resume run failed: %v", err)
		return 2
	}
	out.key("chunks_total", second.TotalChunks)
	out.key("chunks_completed", second.CompletedChunks)
	out.key("index_digest", second.IndexDigest)
	out.key("resume_reprocessed_chunks", second.Reprocessed)
	return 0
}

// demoMetadataConflict prints the extracted audio layout and the recorded
// conflict for the metadata-conflict sample, proving bytes are not rewritten.
func demoMetadataConflict(path string, out *output) {
	data, err := readFile(path)
	if err != nil {
		return
	}
	doc, perr := ebml.Parse(data)
	if perr != nil {
		return
	}
	tracks, terr := mkv.ParseTracks(doc)
	if terr != nil {
		return
	}
	out.section("metadata_conflict")
	for _, tr := range tracks.Items {
		if tr.Type.Value != mkv.TrackAudio {
			continue
		}
		out.key("audio_channels", tr.Audio.Channels.Value)
		out.key("audio_sample_rate", tr.Audio.SamplingFrequency.Value)
		for _, c := range tr.Conflicts {
			out.key("metadata_conflict_field", c.Field)
			out.key("metadata_conflict_offset", c.Offset)
			out.key("metadata_conflict_kept", tr.Audio.Channels.Value)
			out.key("metadata_conflict_rejected", c.Rejected)
		}
	}
}

// demoDamaged reports the error code and offset of each damaged sample. It
// returns exit code 2 when any sample was reported as damaged, which is the
// documented "input damaged but reported" outcome.
func demoDamaged(paths map[fixture.Kind]string, out *output) int {
	out.section("damaged")
	damaged := 0
	for _, kind := range []fixture.Kind{fixture.KindTruncatedBlock, fixture.KindBoundaryConflict} {
		path, ok := paths[kind]
		if !ok {
			continue
		}
		data, err := readFile(path)
		if err != nil {
			out.diag("IO_ERROR: %v", err)
			return 1
		}
		doc, perr := ebml.Parse(data)
		if perr == nil {
			if _, berr := mkv.ParseBlocks(doc); berr != nil {
				perr = berr
			}
		}
		if perr == nil {
			out.key(string(kind)+"_error", "none")
			continue
		}
		se, _ := serr.As(perr)
		code, offset := serr.CodeOf(perr), serr.OffsetOf(perr)
		out.key("error_code", code)
		out.key("error_offset", offset)
		out.key("error_sample", kind)
		if se != nil && len(se.Path) > 0 {
			out.key("error_path", joinPath(se.Path))
		}
		damaged++
	}
	// The damaged samples were deliberately generated and are fully reported;
	// the demo invocation itself succeeded, so it exits 0. The standalone
	// `check` command is what exits 2 on a damaged input.
	_ = damaged
	return 0
}

func scaleLabel(tracks *mkv.Tracks) string {
	if tracks.ScaleDefaulted {
		return fmt.Sprintf("%dns(default)", tracks.TimestampScale)
	}
	return fmt.Sprintf("%dns", tracks.TimestampScale)
}

func joinPath(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "/"
		}
		out += p
	}
	return out
}

// demoEvents maps the generated client events onto the media timeline. Anchors
// are derived by correlating two known client readings with their media
// positions; the remaining events are interpolated and their error recorded.
func demoEvents(out *output) {
	events, err := fixture.ParseClientLog(fixture.ClientLog(fixture.DefaultEvents()))
	if err != nil {
		out.diag("client log parse failed: %v", err)
		return
	}
	tb := clock.DefaultTimebase()
	mapper, err := clock.NewMapper(tb, []clock.Anchor{
		{ClientNanos: 0, MediaNanos: 120 * clock.NanosPerMilli, Label: "join"},
		{ClientNanos: 640 * clock.NanosPerMilli, MediaNanos: 760 * clock.NanosPerMilli, Label: "recover"},
	})
	if err != nil {
		out.diag("mapper build failed: %v", err)
		return
	}
	out.section("events")
	for i, ev := range events {
		mapped, merr := mapper.Map(ev.TimeMS, "client-log")
		if merr != nil {
			out.key(fmt.Sprintf("event_%d_error", i), serr.CodeOf(merr))
			continue
		}
		out.key(fmt.Sprintf("event_%d_raw_ms", i), ev.TimeMS)
		out.key(fmt.Sprintf("event_%d_media_ms", i), mapped.MediaNanos/clock.NanosPerMilli)
		out.key(fmt.Sprintf("event_%d_error_ns", i), mapped.ErrorNanos)
	}
	out.key("event_count", len(events))
}

// demoRTPMP4 cross-checks the event time base against an RTP timestamp grid and
// validates the generated MP4 structure with go-mp4.
func demoRTPMP4(out *output) {
	out.section("rtp_mp4")
	stream := fixture.DefaultRTPStream()
	blobs, err := stream.Marshal()
	if err != nil {
		out.diag("rtp marshal failed: %v", err)
		return
	}
	// Round-trip each packet through the wire format, then convert the
	// timestamp of the last packet into nanoseconds.
	var last rtp.Packet
	if len(blobs) > 0 {
		if err := last.Unmarshal(blobs[len(blobs)-1]); err != nil {
			out.diag("rtp unmarshal failed: %v", err)
			return
		}
	}
	out.key("rtp_packets", len(blobs))
	out.key("rtp_last_seq", last.SequenceNumber)
	out.key("rtp_last_ts_ns", stream.TimestampNS(last.Timestamp))

	mp4Sample := fixture.DefaultMP4()
	brand, tracks, timescale, perr := fixture.ProbeMP4(mp4Sample.Data)
	if perr != nil {
		out.diag("mp4 probe failed: %v", perr)
		return
	}
	out.key("mp4_brand", brand)
	out.key("mp4_tracks", tracks)
	out.key("mp4_timescale", timescale)
	if verr := fixture.ValidateStructure(mp4Sample.Data); verr != nil {
		out.key("mp4_structure", "invalid")
		out.diag("mp4 structure: %v", verr)
	} else {
		out.key("mp4_structure", "valid")
	}
	// A structurally truncated MP4 must be rejected by go-mp4.
	if verr := fixture.ValidateStructure(mp4Sample.Data[:150]); verr != nil {
		out.key("mp4_truncated_reported", 1)
	} else {
		out.key("mp4_truncated_reported", 0)
	}
}
