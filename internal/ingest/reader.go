package ingest

import (
	"bytes"
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"mediaroute/internal/model"
)

// TrackDefaults captures the trex sample defaults declared for a track.
type TrackDefaults struct {
	Duration uint32
	Size     uint32
	Flags    uint32
	Present  bool
}

// ParseInit extracts tracks, timescales, handlers and trex defaults from the
// moov payload. A zero timescale is preserved here and rejected later by
// SourceSet.Validate so the failure carries an error category rather than a
// silently substituted default.
func ParseInit(payload []byte) ([]model.Track, map[uint32]TrackDefaults, error) {
	type partial struct {
		id        uint32
		timescale uint32
		handler   string
	}
	var (
		order    []*partial
		trak     *partial
		defaults = make(map[uint32]TrackDefaults, 4)
	)
	r := bytes.NewReader(payload)
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeMvex(), mp4.BoxTypeMdia():
			_, err := h.Expand()
			return nil, err
		case mp4.BoxTypeTrak():
			trak = &partial{}
			order = append(order, trak)
			_, err := h.Expand()
			trak = nil
			return nil, err
		}
		if !h.BoxInfo.IsSupportedType() {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		switch b := box.(type) {
		case *mp4.Tkhd:
			if trak != nil {
				trak.id = b.TrackID
			}
		case *mp4.Mdhd:
			if trak != nil {
				trak.timescale = b.Timescale
			}
		case *mp4.Hdlr:
			if trak != nil {
				trak.handler = string(b.HandlerType[:])
			}
		case *mp4.Trex:
			defaults[b.TrackID] = TrackDefaults{
				Duration: b.DefaultSampleDuration,
				Size:     b.DefaultSampleSize,
				Flags:    b.DefaultSampleFlags,
				Present:  true,
			}
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, nil, fmt.Errorf("parse moov: %w", err)
	}
	tracks := make([]model.Track, 0, len(order))
	for _, p := range order {
		t := model.Track{TrackID: p.id, Timescale: p.timescale, Handler: p.handler}
		if d, ok := defaults[p.id]; ok {
			t.DefaultDur = d.Duration
			t.DefaultSize = d.Size
			t.DefaultFlags = d.Flags
		}
		tracks = append(tracks, t)
	}
	return tracks, defaults, nil
}

// Config bounds how much input and damage audit Read accepts.
type Config struct {
	// MaxBoxSize rejects box declarations larger than this many bytes.
	MaxBoxSize uint64
	// MaxIssues caps the number of retained issues per source set.
	MaxIssues int
	// MaxInputBytes rejects a whole stream larger than this. It bounds the
	// scanner's input buffer, complementing the per-fragment copies the
	// pipeline already bounds by queue capacity.
	MaxInputBytes uint64
	// Name labels the resulting SourceSet.
	Name string
}

// DefaultConfig returns the configuration used by the demo and tests.
func DefaultConfig() Config {
	return Config{MaxBoxSize: 16 << 20, MaxIssues: 256, MaxInputBytes: 512 << 20, Name: "input"}
}

// pendingFragment is a moof still waiting for its mdat.
type pendingFragment struct {
	moofBox  TopBox
	seq      uint32
	seqKnown bool
	trafs    []parsedTraf
	initSeen bool
}

// Read parses a complete in-memory CMAF/fMP4 stream into a SourceSet. It never
// fails on damaged fragments: damage becomes an isolated fragment with issues,
// and scanning resumes at the next valid boundary. It fails only when the init
// segment is absent or box boundaries cannot be recovered at all.
func Read(data []byte, cfg Config) (*model.SourceSet, error) {
	if cfg.MaxBoxSize == 0 {
		cfg.MaxBoxSize = DefaultConfig().MaxBoxSize
	}
	if cfg.MaxIssues <= 0 {
		cfg.MaxIssues = DefaultConfig().MaxIssues
	}
	if cfg.MaxInputBytes == 0 {
		cfg.MaxInputBytes = DefaultConfig().MaxInputBytes
	}
	if uint64(len(data)) > cfg.MaxInputBytes {
		return nil, model.NewTaskError(model.CategoryInvalidConfig, "ingest",
			fmt.Sprintf("input %d bytes exceeds bound %d", len(data), cfg.MaxInputBytes))
	}
	items, stats := Scan(data, cfg.MaxBoxSize)
	set := &model.SourceSet{Name: cfg.Name}
	set.TrailingBytes = stats.TrailingBytes

	var (
		initBytes     []byte
		initRange     model.ByteRange
		tracks        []model.Track
		defaults      map[uint32]TrackDefaults
		pending       *pendingFragment
		fragIndex     int
		initSeen      bool
		recoveredFrom uint64
		haveRecovered bool
	)
	// finishPending closes a moof that was never followed by its mdat. Such a
	// fragment has no media bytes, so it is isolated rather than delivered.
	finishPending := func() {
		if pending == nil {
			return
		}
		frag := buildFragment(fragIndex, pending, defaults, tracks)
		frag.Truncated = true
		frag.Discarded = true
		frag.AddIssue(model.Issue{
			Category: model.CategoryTruncatedBox,
			Stage:    "ingest/boxes",
			Offset:   pending.moofBox.Header.End(),
			Detail:   "moof was not followed by an mdat",
		})
		set.Fragments = append(set.Fragments, frag)
		fragIndex++
		pending = nil
	}

	for _, it := range items {
		if it.Damage != nil {
			if it.Damage.Recovered {
				recoveredFrom = it.Damage.RecoveredAt
				haveRecovered = true
			}
			// A damaged region that follows a complete moof+mdat pair is
			// simply isolated; if a fragment is still pending, it is closed
			// as truncated so its already-read samples are not delivered.
			if pending != nil {
				frag := buildFragment(fragIndex, pending, defaults, tracks)
				frag.Truncated = true
				frag.Discarded = true
				frag.AddIssue(model.Issue{
					Category:     it.Damage.Category,
					Stage:        "ingest/boxes",
					Offset:       it.Damage.Offset,
					DeclaredSize: it.Damage.DeclaredSize,
					Available:    it.Damage.Available,
					Detail:       it.Damage.Detail,
				})
				if it.Damage.Recovered {
					frag.Recovered = true
				}
				frag.Range = model.RangeOf(pending.moofBox.Header.Offset, it.Damage.Available)
				set.Fragments = append(set.Fragments, frag)
				fragIndex++
				pending = nil
			}
			set.Issues = append(set.Issues, model.Issue{
				Category:     it.Damage.Category,
				Stage:        "ingest/boxes",
				Offset:       it.Damage.Offset,
				DeclaredSize: it.Damage.DeclaredSize,
				Available:    it.Damage.Available,
				Detail:       it.Damage.Detail,
			})
			continue
		}
		box := *it.Box
		switch box.Header.Type {
		case "ftyp":
			if !initSeen {
				initBytes = append(initBytes, data[box.Header.Offset:box.Header.End()]...)
				initRange = model.RangeOf(box.Header.Offset, box.Header.Size)
			}
		case "moov":
			if initSeen {
				continue
			}
			parsedTracks, parsedDefaults, err := ParseInit(box.Payload)
			if err != nil {
				return nil, model.WrapTaskError(model.CategoryInvalidBox, "ingest/init",
					"moov could not be parsed", err).WithOffset(box.Header.Offset)
			}
			tracks, defaults = parsedTracks, parsedDefaults
			initBytes = append(initBytes, data[box.Header.Offset:box.Header.End()]...)
			if initRange.Empty() {
				initRange.Start = box.Header.Offset
			}
			initRange.End = box.Header.End()
			initSeen = true
		case "moof":
			finishPending()
			seq, seqKnown, trafs, err := ParseMoof(box.Payload, box.Header.Offset, box.Header.PayloadOffset(), defaults)
			if err != nil {
				return nil, model.WrapTaskError(model.CategoryInvalidBox, "ingest/fragment",
					"moof could not be parsed", err).WithOffset(box.Header.Offset)
			}
			pending = &pendingFragment{moofBox: box, seq: seq, seqKnown: seqKnown, trafs: trafs, initSeen: initSeen}
		case "mdat":
			if pending == nil {
				continue // orphan mdat: preserved but not associated
			}
			frag := buildFragment(fragIndex, pending, defaults, tracks)
			if haveRecovered && pending.moofBox.Header.Offset >= recoveredFrom {
				frag.Recovered = true
			}
			frag.HasMdat = true
			frag.MdatRange = model.RangeOf(box.Header.Offset, box.Header.Size)
			frag.Range = model.RangeOf(pending.moofBox.Header.Offset,
				box.Header.End()-pending.moofBox.Header.Offset)
			frag.Moof = append([]byte(nil), data[pending.moofBox.Header.Offset:pending.moofBox.Header.End()]...)
			frag.Mdat = append([]byte(nil), data[box.Header.Offset:box.Header.End()]...)
			set.Fragments = append(set.Fragments, frag)
			fragIndex++
			pending = nil
		case "mfra", "sidx":
			// Existing index hints are parsed separately and never trusted.
		default:
			// Preserve every other top-level box - including unknown types -
			// so it can be delivered byte-for-byte. Boxes interleaved after
			// fragments may be media-adjacent metadata, so keeping them is
			// preferable to dropping them silently.
			set.Preserved = append(set.Preserved, model.PreservedBox{
				Type:  box.Header.Type,
				Range: model.RangeOf(box.Header.Offset, box.Header.Size),
				Bytes: append([]byte(nil), data[box.Header.Offset:box.Header.End()]...),
			})
		}
	}
	if pending != nil {
		frag := buildFragment(fragIndex, pending, defaults, tracks)
		frag.Truncated = true
		frag.Discarded = true
		frag.Range = model.RangeOf(pending.moofBox.Header.Offset, pending.moofBox.Header.Size)
		frag.AddIssue(model.Issue{
			Category: model.CategoryTruncatedBox,
			Stage:    "ingest/boxes",
			Offset:   pending.moofBox.Header.End(),
			Detail:   "moof has no following mdat before end of input",
		})
		set.Fragments = append(set.Fragments, frag)
	}
	if initSeen {
		set.Init = &model.InitSegment{Bytes: initBytes, Tracks: tracks}
		set.InitRange = initRange
		set.InitBytes = uint64(len(initBytes))
	}
	set.Tracks = tracks
	existing := ParseExistingIndex(topBoxes(items))
	if existing.Present {
		bad := validateExistingIndex(existing, items)
		set.DamagedIndex = len(bad) > 0
		set.IndexIssues = bad
	}
	// Bytes removed from the delivered stream (an isolated fragment's mdat, or
	// an unrecoverable tail) shrinks the file but not the nominal programme
	// duration. Record their magnitude so the manifest can use the same
	// denominator the target bitrate was defined against.
	var droppedBytes uint64
	for i := range set.Fragments {
		f := &set.Fragments[i]
		if f.Discarded {
			droppedBytes += f.Range.Len()
		}
	}
	set.DroppedBytes = droppedBytes
	if stats.TrailingBytes > 0 {
		set.TrailingBytes = stats.TrailingBytes
		return set, model.NewTaskError(model.CategoryUnrecoverable, "ingest/boxes",
			fmt.Sprintf("%d trailing bytes could not be recovered into a box boundary", stats.TrailingBytes)).
			WithOffset(uint64(len(data)) - stats.TrailingBytes)
	}
	return set, nil
}

func topBoxes(items []ScanItem) []TopBox {
	out := make([]TopBox, 0, len(items))
	for _, it := range items {
		if it.Box != nil {
			out = append(out, *it.Box)
		}
	}
	return out
}

// buildFragment converts a pending moof into a model.Fragment without any
// timeline repair; tfdt values and decode ticks are copied verbatim.
func buildFragment(index int, p *pendingFragment, defaults map[uint32]TrackDefaults, tracks []model.Track) model.Fragment {
	frag := model.Fragment{
		Index:     index,
		Range:     model.RangeOf(p.moofBox.Header.Offset, p.moofBox.Header.Size),
		MoofRange: model.RangeOf(p.moofBox.Header.Offset, p.moofBox.Header.Size),
		Sequence:  p.seq,
	}
	tsByID := make(map[uint32]uint32, len(tracks))
	for _, t := range tracks {
		tsByID[t.TrackID] = t.Timescale
	}
	for i := range p.trafs {
		t := &p.trafs[i]
		ft := model.FragmentTrack{
			TrackID:         t.trackID,
			Timescale:       tsByID[t.trackID],
			Sequence:        p.seq,
			SequenceKnown:   p.seqKnown,
			TfdtPresent:     t.tfdtPresent,
			OriginalTfdt:    t.tfdtValue,
			Tfdt:            t.tfdtValue,
			TfdtVersion:     t.tfdtVersion,
			TfdtPatchAt:     t.tfdtPatchAt,
			DefaultDuration: t.tfhdDefaults.Duration,
			DurationTicks:   t.durationTicks,
			FirstOffset:     t.firstOffset,
			Samples:         t.samples,
		}
		var bytesTotal uint64
		for j := range ft.Samples {
			bytesTotal += uint64(ft.Samples[j].Size)
		}
		ft.SampleBytes = bytesTotal
		frag.Tracks = append(frag.Tracks, ft)
	}
	return frag
}

// validateExistingIndex checks every hint from a pre-existing sidx or mfra
// against the boundaries actually present in the scan. A hint is a hint: any
// offset that does not start an observed box is rejected and recorded, and
// the rebuilt index is constructed independently.
func validateExistingIndex(existing ExistingIndex, items []ScanItem) []model.Issue {
	moofs := make(map[uint64]bool, 8)
	boundaries := make(map[uint64]bool, 16)
	for _, it := range items {
		if it.Box == nil {
			continue
		}
		boundaries[it.Box.Header.Offset] = true
		boundaries[it.Box.Header.End()] = true
		if it.Box.Header.Type == "moof" {
			moofs[it.Box.Header.Offset] = true
		}
	}
	var issues []model.Issue
	for _, ref := range existing.References {
		if !moofs[ref.Offset] && !boundaries[ref.Offset] {
			issues = append(issues, model.Issue{
				Category: model.CategoryCorruptIndex,
				Stage:    "ingest/index-hint",
				Offset:   ref.Offset,
				Detail:   "sidx reference does not land on an observed box boundary",
			})
		}
	}
	for _, ref := range existing.Tfras {
		if !moofs[ref.MoofOffset] {
			issues = append(issues, model.Issue{
				Category: model.CategoryCorruptIndex,
				Stage:    "ingest/index-hint",
				Offset:   ref.MoofOffset,
				TrackID:  ref.TrackID,
				Detail:   "tfra entry does not point at an observed moof",
			})
		}
	}
	return issues
}
