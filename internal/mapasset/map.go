// Package mapasset maps versioned media assets onto tracks and time
// intervals. Each asset is an MP4 file whose track structure is read with
// github.com/abema/go-mp4. Mapping reports every version that fails to
// cover the requested interval, merges overlaps whose content digest
// matches, and quarantines overlaps whose digests differ while recording
// both hashes.
package mapasset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/abema/go-mp4"

	"replay/internal/fault"
)

// Interval is a half-open time range in nanoseconds on the unified
// timeline.
type Interval struct {
	Start int64
	End   int64
}

// Duration returns End-Start, never negative.
func (i Interval) Duration() int64 {
	if i.End <= i.Start {
		return 0
	}
	return i.End - i.Start
}

// Overlaps reports whether two intervals share any instant.
func (i Interval) Overlaps(other Interval) bool {
	return i.Start < other.End && other.Start < i.End
}

// Contains reports whether outer fully covers inner.
func (i Interval) Contains(inner Interval) bool {
	return i.Start <= inner.Start && inner.End <= i.End
}

// String renders the interval as [start,end).
func (i Interval) String() string { return fmt.Sprintf("[%d,%d)", i.Start, i.End) }

// Asset is one versioned MP4 file mapped to a track.
type Asset struct {
	Version   string
	Track     string
	Path      string
	Interval  Interval
	Digest    string
	Timescale uint32
	TrackID   uint32
	// SampleCount is the number of media samples declared by the MP4
	// track.
	SampleCount int
}

// Missing records a range a given version failed to cover.
type Missing struct {
	Version  string
	Track    string
	Interval Interval
	Reason   string
}

// Conflict records two overlapping assets with differing content.
type Conflict struct {
	Track        string
	Interval     Interval
	LeftVersion  string
	RightVersion string
	LeftHash     string
	RightHash    string
	Quarantined  bool
}

// Slot is one published track/interval cell.
type Slot struct {
	Track      string
	Interval   Interval
	Version    string
	Digest     string
	MergedFrom []string
}

// Result is the deterministic mapping output.
type Result struct {
	Slots     []Slot
	Missing   []Missing
	Conflicts []Conflict
	// Merged counts elementary segments folded because at least two
	// versions supplied identical content.
	Merged int
	// Quarantined is the total duration, in nanoseconds, withheld from
	// publication because of conflicts.
	Quarantined int64
}

// ConflictCount returns quarantined conflicts.
func (r *Result) ConflictCount() int { return len(r.Conflicts) }

// MissingCount returns missing ranges.
func (r *Result) MissingCount() int { return len(r.Missing) }

// Options configures a mapping run.
type Options struct {
	// Versions restricts mapping to these versions, in order. Empty
	// means every version present in the assets.
	Versions []string
}

// ProbeAsset reads an MP4 file and returns an asset whose interval starts
// at offsetNS. The file digest covers the whole file, so identical assets
// produce identical digests. The movie duration is converted to
// nanoseconds with the movie timescale.
func ProbeAsset(version, track, path string, read io.ReaderAt, size int64, offsetNS int64) (Asset, error) {
	if version == "" {
		return Asset{}, fault.New(fault.KindArgument, "asset requires a version").At("map")
	}
	if track == "" {
		return Asset{}, fault.New(fault.KindArgument, "asset requires a track").At("map")
	}
	if offsetNS < 0 {
		return Asset{}, fault.New(fault.KindArgument, "asset offset must not be negative").
			At("map").WithVersion(version).WithTrack(track)
	}

	info, err := mp4.Probe(io.NewSectionReader(read, 0, size))
	if err != nil {
		return Asset{}, fault.New(fault.KindInput, "cannot read mp4 structure").
			At("map").WithVersion(version).WithTrack(track).Wrap(err)
	}
	if info.Timescale == 0 {
		return Asset{}, fault.New(fault.KindInput, "mp4 movie timescale is zero").
			At("map").WithVersion(version).WithTrack(track)
	}
	if len(info.Tracks) == 0 {
		return Asset{}, fault.New(fault.KindInput, "mp4 has no tracks").
			At("map").WithVersion(version).WithTrack(track)
	}
	first := info.Tracks[0]
	if first.Timescale == 0 {
		return Asset{}, fault.New(fault.KindInput, "mp4 media timescale is zero").
			At("map").WithVersion(version).WithTrack(track)
	}

	durationNS := int64(info.Duration) * 1_000_000_000 / int64(info.Timescale)
	if durationNS <= 0 {
		return Asset{}, fault.New(fault.KindInput, "mp4 duration is not positive").
			At("map").WithVersion(version).WithTrack(track)
	}

	// The content digest covers the media essence (the sample bytes in
	// mdat), not the container. Two versions may wrap identical media in
	// different headers, and those must still merge.
	digest, err := digestSamples(read, info)
	if err != nil {
		return Asset{}, fault.New(fault.KindInput, "cannot hash asset samples").
			At("map").WithVersion(version).WithTrack(track).Wrap(err)
	}

	return Asset{
		Version:     version,
		Track:       track,
		Path:        path,
		Interval:    Interval{Start: offsetNS, End: offsetNS + durationNS},
		Digest:      digest,
		Timescale:   info.Timescale,
		TrackID:     first.TrackID,
		SampleCount: len(first.Samples),
	}, nil
}

// ProbeFile is the path-based convenience wrapper for ProbeAsset.
func ProbeFile(version, track, path string, offsetNS int64) (Asset, error) {
	f, err := openFile(path)
	if err != nil {
		return Asset{}, fault.New(fault.KindInput, "cannot open asset").
			At("map").WithVersion(version).WithTrack(track).Wrap(err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return Asset{}, fault.New(fault.KindInput, "cannot stat asset").
			At("map").WithVersion(version).WithTrack(track).Wrap(err)
	}
	return ProbeAsset(version, track, path, f, stat.Size(), offsetNS)
}

// digestSamples hashes the media sample bytes referenced by the first
// track's chunk offsets, in track order. The container metadata is
// deliberately excluded so that re-encoded headers carrying the same
// essence compare equal and can be merged.
func digestSamples(read io.ReaderAt, info *mp4.ProbeInfo) (string, error) {
	h := sha256.New()
	track := info.Tracks[0]
	sample := 0
	hashed := 0
	for _, chunk := range track.Chunks {
		start := sample
		end := sample + int(chunk.SamplesPerChunk)
		if end > len(track.Samples) {
			return "", fmt.Errorf("mp4 chunk claims %d samples but only %d remain",
				chunk.SamplesPerChunk, len(track.Samples)-start)
		}
		var chunkSize uint64
		for i := start; i < end; i++ {
			chunkSize += uint64(track.Samples[i].Size)
		}
		if chunkSize == 0 {
			return "", fmt.Errorf("mp4 chunk at offset %d declares no sample bytes", chunk.DataOffset)
		}
		n, err := io.Copy(h, io.NewSectionReader(read, int64(chunk.DataOffset), int64(chunkSize)))
		if err != nil {
			return "", err
		}
		if uint64(n) != chunkSize {
			return "", io.ErrUnexpectedEOF
		}
		hashed += int(chunkSize)
		sample = end
	}
	if hashed == 0 {
		return "", fmt.Errorf("mp4 track contains no sample bytes to hash")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Map folds per-version assets for one track into published slots over
// want. It performs two passes:
//
//  1. Per-version coverage. Every gap a version leaves inside want is a
//     Missing record naming that version.
//  2. Cross-version merge. The covered range is swept into elementary
//     segments. A segment covered by identical digests is published (and
//     counted as merged when more than one version contributed); a
//     segment covered by differing digests is quarantined as a Conflict
//     and not published.
//
// Output is ordered by interval start, then version, so identical inputs
// yield identical results.
func Map(track string, assets []Asset, want Interval) Result {
	versions := listVersions(assets)
	result := mapTrack(track, assets, want, versions)
	return finalize(result)
}

func mapTrack(track string, assets []Asset, want Interval, versions []string) Result {
	var res Result
	byVersion := make(map[string][]Asset, len(versions))
	for _, a := range assets {
		byVersion[a.Version] = append(byVersion[a.Version], a)
	}

	// Pass 1: union coverage. A hole is a range no version covers, so it
	// is a genuine missing item; the record names the versions that
	// were expected to supply it.
	for _, gap := range gaps(assets, want) {
		res.Missing = append(res.Missing, Missing{
			Version:  strings.Join(versions, ","),
			Track:    track,
			Interval: gap,
			Reason:   "no version covers interval",
		})
	}

	// Pass 2: sweep cross-version coverage.
	bounds := boundarySet(assets, want)
	for i := 0; i+1 < len(bounds); i++ {
		seg := Interval{Start: bounds[i], End: bounds[i+1]}
		if seg.Duration() == 0 {
			continue
		}
		covering := coveringAt(assets, seg)
		if len(covering) == 0 {
			// Already reported per version in pass 1.
			continue
		}
		digestSet := map[string][]Asset{}
		for _, a := range covering {
			digestSet[a.Digest] = append(digestSet[a.Digest], a)
		}
		if len(digestSet) == 1 {
			winner := covering[0]
			merged := uniqueVersions(covering)
			slot := Slot{
				Track:    track,
				Interval: seg,
				Version:  winner.Version,
				Digest:   winner.Digest,
			}
			if len(merged) > 1 {
				slot.MergedFrom = merged
			}
			res.Slots = append(res.Slots, slot)
			continue
		}
		// Differing content: quarantine and record both hashes.
		conflict := representativeConflict(track, seg, covering)
		conflict.Quarantined = true
		res.Conflicts = append(res.Conflicts, conflict)
		res.Quarantined += seg.Duration()
	}
	return res
}

// finalize coalesces adjacent records with identical identity so a single
// asset spanning several boundaries appears as one slot.
func finalize(res Result) Result {
	res.Slots = coalesceSlots(res.Slots)
	res.Missing = coalesceMissing(res.Missing)
	res.Conflicts = coalesceConflicts(res.Conflicts)
	for _, s := range res.Slots {
		if len(s.MergedFrom) > 1 {
			res.Merged++
		}
	}
	return res
}

func coalesceSlots(in []Slot) []Slot {
	sort.SliceStable(in, func(i, j int) bool { return lessSlot(in[i], in[j]) })
	var out []Slot
	for _, s := range in {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.Interval.End == s.Interval.Start && last.Track == s.Track &&
				last.Version == s.Version && last.Digest == s.Digest {
				last.Interval.End = s.Interval.End
				last.MergedFrom = unionStrings(last.MergedFrom, s.MergedFrom)
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

func coalesceMissing(in []Missing) []Missing {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Track != in[j].Track {
			return in[i].Track < in[j].Track
		}
		if in[i].Version != in[j].Version {
			return in[i].Version < in[j].Version
		}
		return in[i].Interval.Start < in[j].Interval.Start
	})
	var out []Missing
	for _, m := range in {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.Track == m.Track && last.Version == m.Version && last.Interval.End == m.Interval.Start {
				last.Interval.End = m.Interval.End
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func coalesceConflicts(in []Conflict) []Conflict {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Track != in[j].Track {
			return in[i].Track < in[j].Track
		}
		if in[i].LeftVersion != in[j].LeftVersion {
			return in[i].LeftVersion < in[j].LeftVersion
		}
		if in[i].RightVersion != in[j].RightVersion {
			return in[i].RightVersion < in[j].RightVersion
		}
		return in[i].Interval.Start < in[j].Interval.Start
	})
	var out []Conflict
	for _, c := range in {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.Track == c.Track && last.LeftVersion == c.LeftVersion &&
				last.RightVersion == c.RightVersion && last.LeftHash == c.LeftHash &&
				last.RightHash == c.RightHash && last.Interval.End == c.Interval.Start {
				last.Interval.End = c.Interval.End
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func lessSlot(a, b Slot) bool {
	if a.Interval.Start != b.Interval.Start {
		return a.Interval.Start < b.Interval.Start
	}
	if a.Version != b.Version {
		return a.Version < b.Version
	}
	return a.Digest < b.Digest
}

// boundarySet returns every clipped interval endpoint plus the want
// endpoints, sorted and de-duplicated.
func boundarySet(assets []Asset, want Interval) []int64 {
	set := map[int64]bool{want.Start: true, want.End: true}
	for _, a := range assets {
		for _, edge := range []int64{a.Interval.Start, a.Interval.End} {
			clipped := edge
			if clipped < want.Start {
				clipped = want.Start
			}
			if clipped > want.End {
				clipped = want.End
			}
			set[clipped] = true
		}
	}
	out := make([]int64, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// coveringAt returns assets whose interval fully contains seg, sorted by
// version.
func coveringAt(assets []Asset, seg Interval) []Asset {
	var out []Asset
	for _, a := range assets {
		if a.Interval.Contains(seg) {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// gaps returns the sub-ranges of want not covered by any interval.
func gaps(assets []Asset, want Interval) []Interval {
	var covered []Interval
	for _, a := range assets {
		start := max64(a.Interval.Start, want.Start)
		end := min64(a.Interval.End, want.End)
		if end > start {
			covered = append(covered, Interval{Start: start, End: end})
		}
	}
	sort.Slice(covered, func(i, j int) bool { return covered[i].Start < covered[j].Start })

	var out []Interval
	cursor := want.Start
	for _, c := range covered {
		if c.Start > cursor {
			out = append(out, Interval{Start: cursor, End: c.Start})
		}
		if c.End > cursor {
			cursor = c.End
		}
	}
	if cursor < want.End {
		out = append(out, Interval{Start: cursor, End: want.End})
	}
	return out
}

// representativeConflict picks the two lexicographically first versions
// with differing digests so the record is deterministic.
func representativeConflict(track string, seg Interval, covering []Asset) Conflict {
	first := covering[0]
	for _, a := range covering[1:] {
		if a.Digest != first.Digest {
			return Conflict{
				Track:        track,
				Interval:     seg,
				LeftVersion:  first.Version,
				RightVersion: a.Version,
				LeftHash:     first.Digest,
				RightHash:    a.Digest,
			}
		}
	}
	return Conflict{Track: track, Interval: seg, LeftVersion: first.Version, RightVersion: first.Version}
}

func listVersions(assets []Asset) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range assets {
		if !seen[a.Version] {
			seen[a.Version] = true
			out = append(out, a.Version)
		}
	}
	sort.Strings(out)
	return out
}

func uniqueVersions(assets []Asset) []string {
	return listVersions(assets)
}

// unionStrings merges two version lists into sorted, de-duplicated form.
func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, group := range [][]string{a, b} {
		for _, v := range group {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Describe renders a slot for terminal output.
func (s Slot) Describe() string {
	return fmt.Sprintf("%s %s version=%s digest=%s merged_from=%s",
		s.Track, s.Interval, s.Version, ShortDigest(s.Digest), strings.Join(s.MergedFrom, "+"))
}

// ShortDigest abbreviates a hex digest for logs.
func ShortDigest(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

// MissingVersion builds a Missing record for a version that entirely
// lacks a track. Callers use it when a requested version has no asset for
// the track at all, which is distinct from an uncovered sub-range.
func MissingVersion(version, track string, want Interval) Missing {
	return Missing{
		Version:  version,
		Track:    track,
		Interval: want,
		Reason:   "version has no asset for track",
	}
}

// VersionsFromCSV splits a comma-separated version argument, trimming
// whitespace and rejecting empty entries.
func VersionsFromCSV(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		v := strings.TrimSpace(part)
		if v == "" {
			return nil, fault.New(fault.KindArgument, "empty version in --versions").At("map")
		}
		if seen[v] {
			return nil, fault.New(fault.KindArgument, "duplicate version "+v).At("map")
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}
