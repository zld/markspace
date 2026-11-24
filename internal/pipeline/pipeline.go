// Package pipeline wires probe, recovery, mapping, demux, remux and
// verification into the run the demo and the tests exercise. It keeps the
// data flow explicit: container metadata becomes a track graph and an
// explicit timeline, mapping resolves dependency order, recovery rebuilds the
// timeline where the index cannot be trusted, and only then is anything
// written.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"archive/internal/checkpoint"
	"archive/internal/demux"
	"archive/internal/fault"
	"archive/internal/maptrack"
	"archive/internal/model"
	"archive/internal/probe"
	"archive/internal/recover"
	"archive/internal/remux"
)

// Source is one input file with its container kind.
type Source struct {
	Name   string
	Format model.ContainerKind
	Data   []byte
}

// Ingested is the per-container result of probing, recovery and demux.
type Ingested struct {
	Container model.ContainerKind
	Path      string
	SHA256    string
	File      *probe.File
	Scans     map[string]*recover.TrackScan
	Trusted   map[string]bool
	Tracks    map[string]*demux.TrackData
	Timeline  map[string]*model.TrackTimeline
	Faults    fault.List
}

// AvailableTracks returns the logical tracks that yielded at least one packet,
// in stable order.
func (in *Ingested) AvailableTracks() []string {
	var out []string
	for key, td := range in.Tracks {
		if len(td.Packets) > 0 {
			out = append(out, in.File.LogicalOf(key))
		}
	}
	sort.Strings(out)
	return out
}

// Run is the whole pipeline result for one input set.
type Run struct {
	Inputs    []*Ingested
	Map       *maptrack.Result
	Selected  map[string]*demux.TrackData
	Timeline  *model.Timeline
	Faults    fault.List
	Available []string
}

// LogicalKeys lists the mapping order.
func (r *Run) LogicalKeys() []string {
	if r.Map == nil {
		return nil
	}
	return append([]string(nil), r.Map.Order...)
}

// Ingest probes one source, decides per track between index use and scan, and
// demuxes whatever could be validated.
func Ingest(src Source) (*Ingested, error) {
	f, err := probe.Probe(probe.Source{Name: src.Name, Data: src.Data}, src.Format)
	if err != nil {
		return nil, err
	}
	in := &Ingested{
		Container: src.Format,
		Path:      src.Name,
		SHA256:    probe.Source{Name: src.Name, Data: src.Data}.Digest(),
		File:      f,
		Scans:     map[string]*recover.TrackScan{},
		Trusted:   map[string]bool{},
		Tracks:    map[string]*demux.TrackData{},
		Timeline:  map[string]*model.TrackTimeline{},
	}
	in.Faults = append(in.Faults, f.Faults...)
	need, trusted := recover.Plan(f)
	for _, t := range trusted {
		in.Trusted[t.Key] = true
	}
	// Trusted tracks go through the strict demux, which re-validates every
	// payload; a fragment that fails becomes a fault, not a silent drop.
	for _, t := range trusted {
		td, faults := demuxTrackFromFragments(src, t)
		in.Tracks[t.Key] = td
		in.Faults = append(in.Faults, faults...)
		in.Timeline[t.Key] = timelineFromTrack(t, td)
	}
	// Everything else is scanned. The scan's complete frames are the only
	// ones that can be written; damaged and partial frames stay reported as
	// unrecovered intervals.
	for _, t := range need {
		scan, faults := recover.ScanTrack(src.Data, t)
		in.Scans[t.Key] = scan
		in.Faults = append(in.Faults, faults...)
		td := &demux.TrackData{Key: t.Key, Logical: t.Logical, Kind: t.Kind, Timebase: t.Timebase}
		for _, fr := range scan.Frames {
			if fr.State != recover.StateComplete {
				td.Skipped++
				continue
			}
			sum := sha256.Sum256(fr.Payload)
			td.Packets = append(td.Packets, &demux.Packet{
				TrackKey: t.Key, TrackID: t.TrackID, Seq: int(fr.Seq),
				StartTick: fr.StartTick, Duration: fr.Duration, Timebase: t.Timebase,
				Payload: fr.Payload, SHA256: hex.EncodeToString(sum[:]),
				Sync: fr.Sync, SourceOffset: fr.Wrapper, Valid: true,
			})
		}
		in.Tracks[t.Key] = td
		var report model.TrackTimeline
		recover.Rebuild(scan, &report)
		report.Logical = t.Logical
		in.Timeline[t.Key] = normalizeTimeline(t, td, &report)
	}
	return in, nil
}

// demuxTrackFromFragments is demux.Demux restricted to one track, reusing the
// same validation rules.
func demuxTrackFromFragments(src Source, t *model.Track) (*demux.TrackData, fault.List) {
	tmp := &probe.File{Container: src.Format, Path: src.Name, Size: uint64(len(src.Data)), Tracks: []*model.Track{t}}
	res, err := demux.Demux(probe.Source{Name: src.Name, Data: src.Data}, tmp)
	if err != nil || len(res.Tracks) == 0 {
		return &demux.TrackData{Key: t.Key, Logical: t.Logical, Kind: t.Kind, Timebase: t.Timebase}, nil
	}
	return res.Tracks[0], res.Faults
}

// timelineFromTrack builds an explicit timeline from a trusted track's
// validated packets. Each packet contributes its own segment so the commit
// gate can check a fragment's exact interval.
func timelineFromTrack(t *model.Track, td *demux.TrackData) *model.TrackTimeline {
	report := &model.TrackTimeline{
		Key: t.Key, Logical: t.Logical, Container: string(t.Container), TrackID: t.TrackID,
		Timebase: t.Timebase, Complete: len(td.Packets) == len(t.Fragments),
	}
	for _, p := range td.Packets {
		report.Segments = append(report.Segments, model.Segment{
			TrackKey: t.Key, Source: "index", StartTick: p.StartTick, Duration: p.Duration,
			StartNS: p.Timebase.TickToNanos(p.StartTick), EndNS: p.Timebase.TickToNanos(p.StartTick + p.Duration),
			Recovered: true,
		})
	}
	if len(td.Packets) > 0 {
		r := td.Range()
		report.Declared = model.NanosRange{StartNS: td.Timebase.TickToNanos(r.StartTick), EndNS: td.Timebase.TickToNanos(r.StartTick + r.Duration)}
		report.Recovered = report.Declared
	}
	if !report.Complete {
		report.UnrecoveredNS = append(report.UnrecoveredNS, model.NanosRange{StartNS: report.Declared.StartNS, EndNS: report.Declared.EndNS})
	}
	return report
}

func normalizeTimeline(t *model.Track, td *demux.TrackData, rebuilt *model.TrackTimeline) *model.TrackTimeline {
	rebuilt.Logical = t.Logical
	rebuilt.Container = string(t.Container)
	rebuilt.TrackID = t.TrackID
	if len(td.Packets) == 0 {
		rebuilt.Complete = false
	}
	return rebuilt
}

// mapOptions derives mapping requirements: video and audio are required, and
// any declared dependency is carried through.
func mapOptions() maptrack.Options {
	return maptrack.Options{RequireTracks: []string{"video", "audio"}}
}

// Map joins the ingested containers and selects the payload stream for each
// logical track: the highest-priority container (MXF > MP4) that has packets.
func Map(ins []*Ingested) (*Run, error) {
	files := make([]*probe.File, 0, len(ins))
	for _, in := range ins {
		files = append(files, in.File)
	}
	return mapWithFiles(ins, files, mapOptions())
}

// mapWith is exposed to tests so a synthetic dependency set can be mapped.
func mapWith(files []*probe.File, opt maptrack.Options) (*maptrack.Result, error) {
	return maptrack.Map(files, opt)
}

func mapWithFiles(ins []*Ingested, files []*probe.File, opt maptrack.Options) (*Run, error) {
	m, err := maptrack.Map(files, opt)
	if err != nil {
		return nil, err
	}
	run := &Run{Inputs: ins, Map: m, Selected: map[string]*demux.TrackData{}, Timeline: model.New()}
	// Index every track by logical identity across containers.
	byLogical := map[string][]struct {
		in *Ingested
		td *demux.TrackData
		k  int
	}{}
	for _, in := range ins {
		for key, td := range in.Tracks {
			p := probeFilePriority(in.File, key)
			byLogical[td.Logical] = append(byLogical[td.Logical], struct {
				in *Ingested
				td *demux.TrackData
				k  int
			}{in: in, td: td, k: p})
		}
	}
	for _, logical := range m.Order {
		cands := byLogical[logical]
		if len(cands) == 0 {
			continue
		}
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].k != cands[j].k {
				return cands[i].k > cands[j].k
			}
			if cands[i].td.Key != cands[j].td.Key {
				return cands[i].td.Key < cands[j].td.Key
			}
			return len(cands[i].td.Packets) > len(cands[j].td.Packets)
		})
		best := cands[0]
		run.Selected[logical] = best.td
		// Register every member's explicit timeline; the chosen member is
		// what remux commits.
		for _, c := range cands {
			tt := c.in.Timeline[c.td.Key]
			if tt == nil {
				continue
			}
			if _, exists := run.Timeline.Tracks[tt.Key]; exists {
				continue
			}
			if err := run.Timeline.AddTrack(tt); err != nil {
				return nil, err
			}
		}
		run.Available = append(run.Available, logical)
	}
	for _, in := range ins {
		run.Faults = append(run.Faults, in.Faults...)
	}
	run.Faults = run.Faults.Dedup()
	return run, nil
}

func probeFilePriority(f *probe.File, key string) int {
	for _, t := range f.Tracks {
		if t.Key == key {
			return maptrack.SourcePriority(t.Source)
		}
	}
	return 0
}

// Remux builds the lossless output for the run. Order follows the mapping so
// dependencies are emitted first.
func Remux(run *Run, opt remux.Options) (*remux.Result, error) {
	var tracks []*remux.TrackSource
	id := uint32(1)
	for _, logical := range run.Map.Order {
		td := run.Selected[logical]
		if td == nil || len(td.Packets) == 0 {
			continue
		}
		mapped := mappedByLogical(run.Map, logical)
		src := &remux.TrackSource{
			Key: td.Key, Logical: logical, Kind: td.Kind,
			TrackID: id, Timebase: td.Timebase, Packets: td.Packets,
		}
		id++
		if mapped != nil {
			src.Rotation = mapped.Rotation
			src.Crop = mapped.Crop
			src.Color = mapped.Color
		}
		// Preserve the original codec family from the probed track.
		for _, in := range run.Inputs {
			for _, t := range in.File.Tracks {
				if t.Key == td.Key {
					src.Codec = t.Codec
					src.TrackID = t.TrackID
				}
			}
		}
		if src.Codec == "" {
			if td.Kind == model.KindAudio {
				src.Codec = "mp4a"
			} else {
				src.Codec = "avc1"
			}
		}
		tracks = append(tracks, src)
	}
	return remux.Write(run.Timeline, tracks, opt)
}

func mappedByLogical(m *maptrack.Result, logical string) *maptrack.MappedTrack {
	for _, t := range m.Tracks {
		if t.Logical == logical {
			return t
		}
	}
	return nil
}

// PayloadDigest is a deterministic aggregate of every selected track's payload
// hashes. It is the value reported as payload_sha256.
func PayloadDigest(selected map[string]*demux.TrackData) string {
	keys := make([]string, 0, len(selected))
	for k, td := range selected {
		if len(td.Packets) == 0 {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		td := selected[k]
		// The digest covers logical identity and payload bytes only. The
		// container key and packet sequence intentionally do not participate:
		// a remuxed track has a new key and may legitimately renumber, while
		// the payload identity is what losslessness means.
		fmt.Fprintf(h, "%s\x00%d\n", k, len(td.Packets))
		for _, p := range td.Packets {
			fmt.Fprintf(h, "%s\n", p.SHA256)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PayloadDigestOfResult computes the same aggregate from a demuxed output so
// input and output can be compared directly.
func PayloadDigestOfResult(res *demux.Result) string {
	m := map[string]*demux.TrackData{}
	for _, td := range res.Tracks {
		if len(td.Packets) > 0 {
			m[td.Logical] = td
		}
	}
	return PayloadDigest(m)
}

// CombinedTimeline is the union of every track's timeline, used by reports.
func (r *Run) CombinedTimeline() *model.Timeline { return r.Timeline }

// InputDigests describes the sources for checkpoint binding.
func InputDigests(ins []*Ingested) []checkpoint.InputDigest {
	out := make([]checkpoint.InputDigest, 0, len(ins))
	for _, in := range ins {
		out = append(out, checkpoint.InputDigest{Name: in.Path, Format: string(in.Container), SHA256: in.SHA256})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FaultLines renders every fault deterministically for terminal output.
func FaultLines(l fault.List) []string {
	l = append(fault.List(nil), l...)
	l = l.Dedup()
	lines := make([]string, 0, len(l))
	for _, e := range l {
		lines = append(lines, e.Line())
	}
	return lines
}

// DescribeRun is a stable one-line summary of the mapping.
func (r *Run) DescribeRun() string {
	parts := []string{
		fmt.Sprintf("track_count=%d", r.TrackCount()),
		fmt.Sprintf("mapped_count=%d", r.MappedWithPayload()),
	}
	return strings.Join(parts, " ")
}

// TrackCount is the number of probed tracks across all inputs.
func (r *Run) TrackCount() int {
	n := 0
	for _, in := range r.Inputs {
		n += len(in.File.Tracks)
	}
	return n
}

// MappedWithPayload counts mapping entries that produced a payload stream.
func (r *Run) MappedWithPayload() int {
	n := 0
	for _, t := range r.Map.Tracks {
		if td := r.Selected[t.Logical]; td != nil && len(td.Packets) > 0 {
			n++
		}
	}
	return n
}
