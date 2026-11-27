// Package pipeline orchestrates the continuity run: fixture generation,
// Matroska parsing, RTP recovery, track normalization, pooled
// processing, deterministic merge, A/V sync, checkpointing and the
// manifest. All stage transitions and counters are reported through
// the emit callback.
package pipeline

import (
	"context"
	"fmt"
	"hash/crc32"
	"strings"

	pionrtp "github.com/pion/rtp"

	"continuity/internal/cerr"
	"continuity/internal/fixture"
	"continuity/internal/manifest"
	"continuity/internal/matroska"
	"continuity/internal/media"
	"continuity/internal/merge"
	"continuity/internal/pool"
	"continuity/internal/rtp"
	"continuity/internal/state"
	"continuity/internal/syncer"
)

// Exit codes of the continuity tool.
const (
	ExitOK       = 0
	ExitInput    = 2 // corrupt input or invalid arguments
	ExitInternal = 3 // cancelled or internal error
)

// Options parameterizes one run.
type Options struct {
	Seed           int64
	Workers        int
	Queue          int
	Case           string // "", "truncated-block", "unknown-length-boundary", "cancel-after"
	CancelAfter    int
	CheckpointPath string
	ManifestPath   string
	Resume         bool
}

// Result summarizes a finished run.
type Result struct {
	ExitCode       int
	Events         int
	Committed      int
	ManifestSHA256 string
}

type rawEvent struct {
	ev      merge.Event
	payload []byte
}

// Run executes the pipeline, emitting one line per stage transition
// and counter through emit.
func Run(opts Options, emit func(string)) (r Result) {
	fail := func(code int, format string, args ...any) Result {
		emit(fmt.Sprintf(format, args...))
		return Result{ExitCode: code}
	}
	var cp state.Checkpoint
	if opts.Resume {
		c, ok, err := state.Load(opts.CheckpointPath)
		if err != nil {
			return fail(ExitInput, "error=checkpoint_unreadable path=%s", opts.CheckpointPath)
		}
		if !ok {
			return fail(ExitInput, "error=checkpoint_not_found path=%s", opts.CheckpointPath)
		}
		cp = c
		emit(fmt.Sprintf("phase=resume status=ok committed=%d last_timestamp_ns=%d", cp.CommittedEvents, cp.LastTimestampNs))
	}
	corrupt := ""
	switch opts.Case {
	case "", "cancel-after":
	case "truncated-block", "unknown-length-boundary":
		corrupt = opts.Case
	default:
		return fail(ExitInput, "error=invalid_case case=%s", opts.Case)
	}
	if opts.Resume {
		corrupt = ""
	}
	in := fixture.Generate(opts.Seed, corrupt)
	emit(fmt.Sprintf("phase=generate status=ok seed=%d matroska_bytes=%d mp4_bytes=%d rtp_packets=%d",
		opts.Seed, len(in.Matroska), len(in.MP4Init), len(in.RTP)))

	seg, err := matroska.Parse(in.Matroska)
	if err != nil {
		code, off, path := cerr.Locate(err)
		emit(fmt.Sprintf("phase=parse status=error code=%s offset=%d path=%s", code, off, path))
		m := &manifest.Manifest{
			Seed:        opts.Seed,
			Events:      len(seg.Blocks),
			Termination: fmt.Sprintf("%s at %s offset=%d", code, path, off),
		}
		if sha, werr := manifest.Write(opts.ManifestPath, m); werr == nil {
			emit(fmt.Sprintf("phase=manifest status=ok path=%s sha256=%s", opts.ManifestPath, sha))
		}
		emit(fmt.Sprintf("recovered_events=%d", len(seg.Blocks)))
		return fail(ExitInput, "error code=%s offset=%d path=%s recovered_events=%d", code, off, path, len(seg.Blocks))
	}
	emit(fmt.Sprintf("phase=parse status=ok tracks=%d blocks=%d", len(seg.Tracks), len(seg.Blocks)))

	colr, err := media.ParseMP4Colour(in.MP4Init)
	if err != nil {
		code, _, path := cerr.Locate(err)
		return fail(ExitInput, "phase=mp4 status=error code=%s path=%s", code, path)
	}

	rec := rtp.NewRecoverer(rtp.DefaultMaxGap)
	var committed []rtp.Committed
	for _, raw := range in.RTP {
		var pkt pionrtp.Packet
		if uerr := pkt.Unmarshal(raw); uerr != nil {
			return fail(ExitInput, "phase=recover status=error code=%s", cerr.CodeInvalidArgument)
		}
		committed = append(committed, rec.Feed(&pkt)...)
	}
	committed = append(committed, rec.Flush()...)
	st := rec.Stats
	emit(fmt.Sprintf("phase=recover status=ok received=%d lost=%d duplicate=%d late=%d",
		st.Received, st.Lost, st.Duplicated, st.Late))

	tracks, kindOf, terr := buildTracks(seg, colr)
	if terr != nil {
		code, _, path := cerr.Locate(terr)
		return fail(ExitInput, "phase=normalize status=error code=%s path=%s", code, path)
	}
	norm, decisions, err := media.Normalize(tracks)
	if err != nil {
		code, _, path := cerr.Locate(err)
		return fail(ExitInput, "phase=normalize status=error code=%s path=%s", code, path)
	}
	emit(fmt.Sprintf("phase=normalize status=ok decisions=%d", len(decisions)))

	raws, rtpBase := buildEvents(seg, committed, kindOf)

	p, perr := pool.New[rawEvent, merge.Event](context.Background(), opts.Workers, opts.Queue,
		func(ctx context.Context, re rawEvent) (merge.Event, error) {
			ev := re.ev
			ev.Checksum = crc32.ChecksumIEEE(re.payload)
			return ev, nil
		})
	if perr != nil {
		return fail(ExitInput, "error=invalid_params %v", perr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for _, re := range raws {
			if p.Submit(ctx, re) != nil {
				return
			}
		}
		p.Close()
	}()
	var primaryEv, backupEv []merge.Event
	for res := range p.Results() {
		if res.Err != nil {
			continue
		}
		if res.Value.Track == 3 {
			backupEv = append(backupEv, res.Value)
		} else {
			primaryEv = append(primaryEv, res.Value)
		}
	}
	if p.Err() != nil {
		return fail(ExitInternal, "phase=process status=error code=%s", cerr.CodeWorkerFailed)
	}
	emit(fmt.Sprintf("phase=process status=ok workers=%d queue=%d", opts.Workers, opts.Queue))

	merge.Sort(primaryEv)
	merge.Sort(backupEv)
	merged := merge.Merge(primaryEv, backupEv)
	emit(fmt.Sprintf("phase=merge status=ok events=%d", len(merged)))

	sum := syncer.Align(merged)
	emit(fmt.Sprintf("phase=sync status=ok pairs=%d max_deviation_ns=%d", sum.Pairs, sum.MaxDeviationNs))

	digest := merge.DigestSeed()
	committedN := 0
	if opts.Resume {
		digest = cp.Digest
		committedN = cp.CommittedEvents
	}
	lastTs := int64(0)
	for i, ev := range merged {
		if i < cp.CommittedEvents {
			continue // already committed in a previous run
		}
		digest = merge.DigestEvent(digest, ev)
		committedN++
		lastTs = ev.TimestampNs
		if !opts.Resume && opts.Case == "cancel-after" && opts.CancelAfter > 0 && committedN >= opts.CancelAfter {
			cancel()
			cp2 := state.Checkpoint{Phase: "commit", CommittedEvents: committedN,
				LastTimestampNs: lastTs, InputOffset: int64(committedN), Digest: digest}
			if serr := state.Save(opts.CheckpointPath, cp2); serr != nil {
				return fail(ExitInternal, "error=checkpoint_save %v", serr)
			}
			emit(fmt.Sprintf("phase=commit status=cancelled committed=%d last_timestamp_ns=%d", committedN, lastTs))
			emit(fmt.Sprintf("cancelled phase=commit committed=%d input_offset=%d", committedN, committedN))
			return Result{ExitCode: ExitInternal, Committed: committedN}
		}
	}
	if lastTs == 0 && len(merged) > 0 {
		lastTs = merged[len(merged)-1].TimestampNs
	}
	emit(fmt.Sprintf("phase=commit status=ok committed=%d", committedN))

	final := state.Checkpoint{Phase: "done", CommittedEvents: committedN,
		LastTimestampNs: lastTs, InputOffset: int64(committedN), Digest: digest}
	if err := state.Save(opts.CheckpointPath, final); err != nil {
		return fail(ExitInternal, "error=checkpoint_save %v", err)
	}
	emit(fmt.Sprintf("phase=checkpoint status=ok path=%s", opts.CheckpointPath))

	m := buildManifest(opts, norm, decisions, st, sum, rtpBase, in, len(merged), digest)
	sha, werr := manifest.Write(opts.ManifestPath, m)
	if werr != nil {
		return fail(ExitInternal, "error=manifest_write %v", werr)
	}
	emit(fmt.Sprintf("phase=manifest status=ok path=%s sha256=%s", opts.ManifestPath, sha))

	var recSeqs []string
	for _, rc := range st.Recoveries {
		recSeqs = append(recSeqs, fmt.Sprint(rc.Seq))
	}
	emit(fmt.Sprintf("recovery_positions=%s", strings.Join(recSeqs, ",")))
	emit(fmt.Sprintf("summary events=%d rtp_received=%d rtp_lost=%d rtp_duplicate=%d rtp_late=%d max_av_deviation_ns=%d manifest_sha256=%s",
		len(merged), st.Received, st.Lost, st.Duplicated, st.Late, sum.MaxDeviationNs, sha))
	return Result{ExitCode: ExitOK, Events: len(merged), Committed: committedN, ManifestSHA256: sha}
}
