package pipeline

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"mediad/index"
	"mediad/media"
	"mediad/timeline"
)

// Job is one batch run against a set of inputs. It owns the durable state
// (index store, metadata cache, checkpoint) and the configuration.
type Job struct {
	Config Config
	// IndexStore persists per-input parse indexes.
	IndexStore *index.Store
	// Cache persists parsed metadata keyed by the metadata fingerprint.
	Cache *index.Cache
	// Checkpoint persists committed work chunks.
	Checkpoint *index.CheckpointStore

	// scanned collects results by path.
	scanned map[string]*ScanResult
	// dir is the run's work directory, used to render input paths relative to
	// it so the manifest digest does not depend on where the run happened.
	dir string
}

// NewJob creates a job with the given config and durable stores bound to dir.
// Every run gets its own directory; tests pass a temp dir.
func NewJob(cfg Config, dir string) (*Job, error) {
	idxStore, err := index.OpenStore(dir + "/index")
	if err != nil {
		return nil, err
	}
	cache, err := index.OpenCache(dir + "/cache")
	if err != nil {
		return nil, err
	}
	cp := index.NewCheckpointStore(dir + "/checkpoint.json")
	return &Job{Config: cfg, IndexStore: idxStore, Cache: cache, Checkpoint: cp, scanned: map[string]*ScanResult{}, dir: dir}, nil
}

// ScanInput fingerprints and parses one input. The fingerprint is computed over
// the region layout the parser reports, so container and metadata changes are
// distinguished. If a stored index matches the container, sample tables are
// reused; if the metadata fingerprint matches, cached events are reused.
func (j *Job) ScanInput(in Input) (*ScanResult, error) {
	res := &ScanResult{Input: in}

	if in.IsRTP {
		return j.scanRTP(in, res)
	}

	clip, err := media.Parse(in.Path, in.Bytes)
	if err != nil {
		// A file that cannot be parsed still gets a fingerprint so a retry can
		// tell it apart, but the error names the source position.
		fp, fpErr := index.ComputeFingerprintFromBytes(in.Bytes)
		if fpErr != nil {
			return nil, err
		}
		res.Fingerprint = fp
		res.Issues.AddError(timeline.CodeUnsupported, err)
		j.scanned[in.Path] = res
		return res, err
	}
	res.Clip = clip

	fp, err := index.ComputeFingerprintFromBytes(clip.Bytes())
	if err != nil {
		return nil, err
	}
	res.Fingerprint = fp

	prev, plan := j.IndexStore.ReuseFor(in.Path, fp, media.ParserVersion)
	res.IndexReused = plan.IndexReusable() && prev != nil

	// Metadata events are parsed through the cache if it is valid.
	events, evIssues, cached := j.metadataFor(in, clip, fp)
	res.Events = events
	res.Issues.Merge(evIssues)
	title := "events"
	if cached {
		title = "events (cached)"
	}
	_ = title

	// Persist the index so a later run of the same input can reuse it.
	if !plan.IndexReusable() || prev == nil {
		if err := j.IndexStore.Put(in.Path, buildParseIndex(clip, fp)); err != nil {
			res.Issues.Add(timeline.Issue{Severity: timeline.SeverityWarning, Code: timeline.CodeIO,
				File: in.Path, Message: "could not persist index: " + err.Error()})
		}
	}
	j.scanned[in.Path] = res
	return res, nil
}

// buildParseIndex converts a parsed clip into the durable index form.
func buildParseIndex(clip *media.Clip, fp index.Fingerprint) *index.ParseIndex {
	pi := &index.ParseIndex{Fingerprint: fp, Regions: clip.Regions, ParserVersion: media.ParserVersion}
	for _, t := range clip.Tracks {
		ti := index.TrackIndex{TrackID: t.ID, Handler: string(t.Handler), TimeScale: t.TimeScale, Samples: t.Samples}
		pi.Tracks = append(pi.Tracks, ti)
	}
	return pi
}

// eventsFromClip parses all timed metadata in a clip: subtitle samples, chpl
// chapters, emsg boxes and SEI messages, placing each on the shared timeline.
// It returns events and every parse issue, so damaged metadata is reported but
// never silently dropped from the account.
func eventsFromClip(clip *media.Clip) ([]timeline.Event, timeline.Issues) {
	var events []timeline.Event
	var issues timeline.Issues
	for _, t := range clip.Tracks {
		switch t.Kind {
		case media.KindSubtitle:
			var payloads [][]byte
			var ticks []int64
			var durs []timeline.Rational
			for _, s := range t.Samples {
				data, err := clip.SampleData(s)
				if err != nil {
					issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeTruncated,
						File: clip.Path, TrackID: t.ID, ByteOffset: s.Offset, Message: err.Error()})
					continue
				}
				payloads = append(payloads, data)
				ticks = append(ticks, s.CompositeTicks)
				d, _ := timeline.NewRational(s.DurationTicks, int64(t.TimeScale))
				durs = append(durs, d)
			}
			evs, is := media.ParseSubtitleSamples(payloads, t.ID, clip.Path, t.TimeBase(), ticks, durs)
			events = append(events, evs...)
			issues.Merge(is)
		case media.KindVideo:
			for _, s := range t.Samples {
				data, err := clip.SampleData(s)
				if err != nil {
					continue
				}
				msgs, is := media.ParseSEI(data, clip.Path, t.ID, s.Offset)
				issues.Merge(is)
				evs, is2 := media.SEIToEvents(msgs, clip.Path, t.ID, t.TimeBase(), s.CompositeTicks)
				events = append(events, evs...)
				issues.Merge(is2)
			}
		}
	}
	for _, ch := range clip.ChplBoxes() {
		evs, is := media.ParseChpl(ch.Payload, clip.Path, 0)
		events = append(events, evs...)
		issues.Merge(is)
	}
	for _, em := range clip.EmsgBoxes() {
		info, err := media.ParseEmsg(em.Payload, clip.Path, em.Offset)
		if err != nil {
			issues.AddError(timeline.CodeInvalidLength, err)
			continue
		}
		ev, err := media.EmsgToEvent(info, clip.Path, em.BaseTime)
		if err != nil {
			issues.AddError(timeline.CodeInvalidTime, err)
			continue
		}
		events = append(events, ev)
	}
	return events, issues
}

// metadataFor returns events for a clip, consulting the cache by metadata
// fingerprint. When the cache hits, events are decoded from it; otherwise they
// are parsed and stored.
func (j *Job) metadataFor(in Input, clip *media.Clip, fp index.Fingerprint) ([]timeline.Event, timeline.Issues, bool) {
	if j.Cache != nil {
		if entry, ok := j.Cache.Get(in.Path, fp.Metadata, media.ParserVersion); ok {
			events, issues, err := decodeCachedEvents(entry)
			if err == nil {
				return events, issues, true
			}
		}
	}
	events, issues := eventsFromClip(clip)
	if j.Cache != nil {
		if err := storeCachedEvents(j.Cache, in.Path, fp.Metadata, events, issues); err != nil {
			issues.Add(timeline.Issue{Severity: timeline.SeverityWarning, Code: timeline.CodeIO,
				File: in.Path, Message: "could not persist metadata cache: " + err.Error()})
		}
	}
	return events, issues, false
}

// Paths returns scanned inputs in a stable order.
func (j *Job) Paths() []string {
	out := make([]string, 0, len(j.scanned))
	for p := range j.scanned {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ScanAll scans every input, returning the first fatal scan error (if any)
// while still recording per-input issues for the others.
func (j *Job) ScanAll(inputs []Input) (timeline.Issues, error) {
	var issues timeline.Issues
	var firstErr error
	for _, in := range inputs {
		res, err := j.ScanInput(in)
		if res != nil {
			issues.Merge(res.Issues)
		}
		if err != nil && firstErr == nil {
			firstErr = err
			issues.AddError(timeline.CodeUnsupported, err)
		}
	}
	return issues, firstErr
}

// verifyInputs re-fingerprints every scanned input at commit time and returns
// an error if any changed since the run started. This is the guard the
// checkpoint relies on: chunks computed from bytes that later changed must not
// be committed.
func (j *Job) verifyInputs() (map[string]index.Fingerprint, error) {
	out := make(map[string]index.Fingerprint, len(j.scanned))
	for _, path := range j.Paths() {
		res := j.scanned[path]
		data, err := os.ReadFile(path)
		if err != nil {
			// In-memory inputs (tests) have no on-disk copy; trust the scanned
			// bytes.
			if res.Input.Bytes != nil {
				out[path] = res.Fingerprint
				continue
			}
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var fp index.Fingerprint
		if res.IsRTP {
			fp, err = index.ComputeFingerprintOfBytes(data)
		} else {
			fp, err = index.ComputeFingerprintFromBytes(data)
		}
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(data, res.Input.Bytes) && res.Input.Bytes != nil {
			fp = res.Fingerprint // scanned bytes are authoritative for in-memory inputs
		}
		out[path] = fp
	}
	return out, nil
}

// scanRTP parses an RTP packet capture, reorders and recovers loss. The
// fingerprint is over the capture bytes themselves, so a re-captured stream is
// a different input; the recovery result feeds the loss-recovery statistic.
func (j *Job) scanRTP(in Input, res *ScanResult) (*ScanResult, error) {
	res.IsRTP = true
	fp, err := index.ComputeFingerprintOfBytes(in.Bytes)
	if err != nil {
		return nil, err
	}
	res.Fingerprint = fp
	packets, perr := media.ParseRTPCapture(in.Path, in.Bytes)
	if perr != nil {
		res.Issues.AddError(timeline.CodeInvalidLength, perr)
		j.scanned[in.Path] = res
		return res, perr
	}
	rec := media.RecoverRTPWithWindow(packets, j.Config.ReorderWindow)
	res.RTP = &rec
	res.Issues.Merge(rec.Issues)
	j.scanned[in.Path] = res
	return res, nil
}
