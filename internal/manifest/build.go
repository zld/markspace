// Package manifest builds the final archive manifest and the content-addressed
// derivative store that sits under it.
//
// Derivatives are layered: layer 1 items normalise one source (a subtitle
// track, a stream analysis report), and layer 2 items combine layer 1 output.
// Every derivative is keyed by its kind, its layer and the digests of its
// dependencies, and it is stored under the digest of its own bytes. A source
// digest change therefore changes exactly the keys that depend on that source;
// nothing else is invalidated.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/timebase"
)

// Version is the manifest schema version.
const Version = 1

// SourceRef identifies a source by digest.
type SourceRef struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// TrackRef is a manifest entry for one analysed track.
type TrackRef struct {
	Source        string         `json:"source"`
	Index         int            `json:"index"`
	ID            uint32         `json:"id"`
	Handler       string         `json:"handler"`
	Timescale     uint32         `json:"timescale"`
	DurationNanos timebase.Nanos `json:"durationNanos"`
	SampleCount   int            `json:"sampleCount"`
	SyncSamples   int            `json:"syncSamples"`
	IntervalNanos timebase.Nanos `json:"intervalNanos"`
}

// Damage records one damaged byte interval of a source.
type Damage struct {
	Source string `json:"source"`
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// TimeRange is the covered time range of a track or subtitle source.
type TimeRange struct {
	Source string         `json:"source"`
	Track  string         `json:"track,omitempty"`
	Start  timebase.Nanos `json:"start"`
	End    timebase.Nanos `json:"end"`
}

// Derivative is one stored, content-addressed artefact.
type Derivative struct {
	Kind   string   `json:"kind"`
	Layer  int      `json:"layer"`
	Key    string   `json:"key"`
	Digest string   `json:"digest"`
	Size   int64    `json:"size"`
	Path   string   `json:"path"`
	Deps   []string `json:"deps"`
}

// Counts summarises a build against the previous run.
type Counts struct {
	Added       int `json:"added"`
	Reused      int `json:"reused"`
	Changed     int `json:"changed"`
	Invalidated int `json:"invalidated"`
}

// Manifest is the stable, self-verifying archive description.
type Manifest struct {
	Version     int          `json:"version"`
	Sources     []SourceRef  `json:"sources"`
	Tracks      []TrackRef   `json:"tracks"`
	Damage      []Damage     `json:"damage"`
	TimeRanges  []TimeRange  `json:"timeRanges"`
	Derivatives []Derivative `json:"derivatives"`
	Counts      Counts       `json:"counts"`
}

// body is the checksum-covered portion of a manifest.
type body struct {
	Version     int          `json:"version"`
	Sources     []SourceRef  `json:"sources"`
	Tracks      []TrackRef   `json:"tracks"`
	Damage      []Damage     `json:"damage"`
	TimeRanges  []TimeRange  `json:"timeRanges"`
	Derivatives []Derivative `json:"derivatives"`
}

// document is what is written to disk: the body plus its checksum and counts.
type document struct {
	body
	Counts   Counts `json:"counts"`
	Checksum string `json:"checksum"`
}

// Checksum returns the SHA-256 of the canonical body encoding. The counts are
// deliberately outside the checksum: they describe the build that produced the
// manifest, not its content, so a first run and a repeat run over unchanged
// sources yield the same checksum.
func (m *Manifest) Checksum() (string, error) {
	data, err := canonical(body{
		Version: m.Version, Sources: m.Sources, Tracks: m.Tracks,
		Damage: m.Damage, TimeRanges: m.TimeRanges, Derivatives: m.Derivatives,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func canonical(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Builder accumulates manifest entries and writes the derivative store.
type Builder struct {
	dir        string
	sources    []SourceRef
	tracks     []TrackRef
	damage     []Damage
	ranges     []TimeRange
	derivs     []Derivative
	seenKeys   map[string]bool
	byDigest   map[string]string
	previous   map[string]Derivative
	prevLoaded bool
	prevErr    error
	counts     Counts
}

// NewBuilder opens a builder rooted at dir. A previous manifest in dir is
// loaded so the next run can report added, reused, changed and invalidated
// derivatives.
func NewBuilder(dir string) *Builder {
	return &Builder{dir: dir, seenKeys: map[string]bool{}, byDigest: map[string]string{}, previous: map[string]Derivative{}}
}

// Dir is the archive output root.
func (b *Builder) Dir() string { return b.dir }

// AddSource records a source. Adding the same digest twice is a no-op.
func (b *Builder) AddSource(src *asset.Source) error {
	if err := checkSource(src); err != nil {
		return err
	}
	for _, s := range b.sources {
		if s.Digest == src.Digest {
			return nil
		}
	}
	b.sources = append(b.sources, SourceRef{Name: src.Name, Digest: src.Digest, Size: src.Size})
	return nil
}

func checkSource(src *asset.Source) error {
	if src == nil {
		return fault.New(fault.StageManifest, fault.KindInvalid, "", 0, "nil source")
	}
	if len(src.Digest) != 64 {
		return fault.New(fault.StageManifest, fault.KindDigest, src.Name, 0, "source digest %q is not SHA-256", src.Digest)
	}
	return nil
}

// AddTrack records a track.
func (b *Builder) AddTrack(t TrackRef) { b.tracks = append(b.tracks, t) }

// AddDamage records a damaged interval.
func (b *Builder) AddDamage(d Damage) { b.damage = append(b.damage, d) }

// AddTimeRange records a covered time range.
func (b *Builder) AddTimeRange(r TimeRange) { b.ranges = append(b.ranges, r) }

// DerivativeSpec describes one derivative to store.
type DerivativeSpec struct {
	Kind  string
	Layer int
	Data  []byte
	// Deps are the SHA-256 digests this artefact was derived from.
	Deps []string
}

// Key computes the content-addressed key for a derivative: the hash of its
// kind, layer and dependency digests. It is independent of the payload, which
// is what lets a rebuild detect a changed payload under an unchanged key.
func Key(kind string, layer int, deps []string) string {
	sorted := append([]string(nil), deps...)
	sort.Strings(sorted)
	h := sha256.New()
	fmt.Fprintf(h, "v%d\x00%s\x00%d\x00", Version, kind, layer)
	for _, d := range sorted {
		fmt.Fprintf(h, "%s\x00", d)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AddDerivative stores the payload under its content digest, records it in the
// manifest and classifies it against the previous run. A key is recorded once:
// repeating it is a no-op, while identical bytes under different keys share a
// single stored file.
func (b *Builder) AddDerivative(spec DerivativeSpec) (Derivative, error) {
	if spec.Kind == "" {
		return Derivative{}, fault.New(fault.StageManifest, fault.KindInvalid, "", 0, "derivative without a kind")
	}
	if spec.Layer < 1 {
		return Derivative{}, fault.New(fault.StageManifest, fault.KindInvalid, "", 0, "derivative %q has layer %d, want >= 1", spec.Kind, spec.Layer)
	}
	for _, dep := range spec.Deps {
		if len(dep) != 64 {
			return Derivative{}, fault.New(fault.StageManifest, fault.KindDigest, "", 0, "derivative %q has dependency %q that is not SHA-256", spec.Kind, dep)
		}
	}
	key := Key(spec.Kind, spec.Layer, spec.Deps)
	sum := sha256.Sum256(spec.Data)
	digest := hex.EncodeToString(sum[:])
	if err := b.ensurePrevious(); err != nil {
		return Derivative{}, err
	}
	path := b.storePath(spec.Kind, digest)
	if _, err := asset.WriteAtomic(filepath.Join(b.dir, filepath.FromSlash(path)), spec.Data); err != nil {
		return Derivative{}, fault.Wrap(err, fault.StageManifest, fault.KindCorrupt, path, 0, "store derivative")
	}
	if _, ok := b.byDigest[digest]; !ok {
		b.byDigest[digest] = path
	}
	deriv := Derivative{
		Kind: spec.Kind, Layer: spec.Layer, Key: key, Digest: digest,
		Size: int64(len(spec.Data)), Path: path, Deps: append([]string(nil), spec.Deps...),
	}
	// An artefact is identified by its key. The same bytes produced under two
	// keys are two artefacts that share one file.
	if b.seenKeys[key] {
		return deriv, nil
	}
	b.seenKeys[key] = true
	if prev, ok := b.previous[key]; ok {
		if prev.Digest == digest {
			b.counts.Reused++
		} else {
			b.counts.Changed++
		}
	} else {
		b.counts.Added++
	}
	b.derivs = append(b.derivs, deriv)
	return deriv, nil
}

// storePath lays derivatives out by kind and digest prefix, so identical
// payloads share a single file regardless of how many keys produced them.
func (b *Builder) storePath(kind, digest string) string {
	if p, ok := b.byDigest[digest]; ok {
		return p
	}
	safe := sanitiseKind(kind)
	return "derivatives/" + safe + "/" + digest[:2] + "/" + digest
}

func sanitiseKind(kind string) string {
	var sb strings.Builder
	for _, r := range kind {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			sb.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			sb.WriteRune(r + 32)
		default:
			sb.WriteByte('-')
		}
	}
	return sb.String()
}

// LoadPrevious reads the manifest currently in the output directory, if any,
// and seeds reuse accounting from it.
func (b *Builder) LoadPrevious() error {
	data, err := os.ReadFile(filepath.Join(b.dir, "manifest.json"))
	if os.IsNotExist(err) {
		b.prevLoaded = true
		return nil
	}
	if err != nil {
		return fault.Wrap(err, fault.StageManifest, fault.KindMissing, "manifest.json", 0, "read previous manifest")
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return fault.Wrap(err, fault.StageManifest, fault.KindCorrupt, "manifest.json", 0, "decode previous manifest")
	}
	for _, d := range doc.Derivatives {
		b.previous[d.Key] = d
	}
	b.prevLoaded = true
	return nil
}

// ensurePrevious loads the previous manifest once, recording any failure so
// later calls see it too.
func (b *Builder) ensurePrevious() error {
	if b.prevLoaded {
		return b.prevErr
	}
	b.prevErr = b.LoadPrevious()
	return b.prevErr
}

// Build finalises the manifest: entries are sorted into a stable order,
// invalidated derivatives are counted, and the document is written atomically.
func (b *Builder) Build() (*Manifest, error) {
	if err := b.ensurePrevious(); err != nil {
		return nil, err
	}
	for key := range b.previous {
		if !b.seenKeys[key] {
			b.counts.Invalidated++
		}
	}
	m := &Manifest{
		Version:     Version,
		Sources:     append([]SourceRef(nil), b.sources...),
		Tracks:      append([]TrackRef(nil), b.tracks...),
		Damage:      append([]Damage(nil), b.damage...),
		TimeRanges:  append([]TimeRange(nil), b.ranges...),
		Derivatives: append([]Derivative(nil), b.derivs...),
		Counts:      b.counts,
	}
	sortManifest(m)
	normalise(m)
	data, err := m.encode()
	if err != nil {
		return nil, err
	}
	if _, err := asset.WriteAtomic(filepath.Join(b.dir, "manifest.json"), data); err != nil {
		return nil, fault.Wrap(err, fault.StageManifest, fault.KindCorrupt, "manifest.json", 0, "write manifest")
	}
	return m, nil
}

func (m *Manifest) encode() ([]byte, error) {
	sum, err := m.Checksum()
	if err != nil {
		return nil, err
	}
	doc := document{
		body: body{
			Version: m.Version, Sources: m.Sources, Tracks: m.Tracks,
			Damage: m.Damage, TimeRanges: m.TimeRanges, Derivatives: m.Derivatives,
		},
		Counts: m.Counts, Checksum: sum,
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fault.Wrap(err, fault.StageManifest, fault.KindCorrupt, "manifest.json", 0, "encode manifest")
	}
	return append(out, '\n'), nil
}

// Bytes renders the manifest exactly as it is written to disk.
func (m *Manifest) Bytes() ([]byte, error) { return m.encode() }

// normalise replaces nil collections with empty ones so a manifest for a
// source with no damage or no tracks encodes identically every time.
func normalise(m *Manifest) {
	if m.Sources == nil {
		m.Sources = []SourceRef{}
	}
	if m.Tracks == nil {
		m.Tracks = []TrackRef{}
	}
	if m.Damage == nil {
		m.Damage = []Damage{}
	}
	if m.TimeRanges == nil {
		m.TimeRanges = []TimeRange{}
	}
	if m.Derivatives == nil {
		m.Derivatives = []Derivative{}
	}
}

// sortManifest puts every collection in the stable order used for checksums.
func sortManifest(m *Manifest) {
	sort.SliceStable(m.Sources, func(i, j int) bool {
		if m.Sources[i].Digest != m.Sources[j].Digest {
			return m.Sources[i].Digest < m.Sources[j].Digest
		}
		return m.Sources[i].Name < m.Sources[j].Name
	})
	sort.SliceStable(m.Tracks, func(i, j int) bool {
		if m.Tracks[i].Source != m.Tracks[j].Source {
			return m.Tracks[i].Source < m.Tracks[j].Source
		}
		if m.Tracks[i].ID != m.Tracks[j].ID {
			return m.Tracks[i].ID < m.Tracks[j].ID
		}
		return m.Tracks[i].Index < m.Tracks[j].Index
	})
	sort.SliceStable(m.Damage, func(i, j int) bool {
		a, c := m.Damage[i], m.Damage[j]
		if a.Source != c.Source {
			return a.Source < c.Source
		}
		if a.Start != c.Start {
			return a.Start < c.Start
		}
		if a.End != c.End {
			return a.End < c.End
		}
		return a.Kind < c.Kind
	})
	sort.SliceStable(m.TimeRanges, func(i, j int) bool {
		a, c := m.TimeRanges[i], m.TimeRanges[j]
		if a.Source != c.Source {
			return a.Source < c.Source
		}
		if a.Track != c.Track {
			return a.Track < c.Track
		}
		return a.Start < c.Start
	})
	sort.SliceStable(m.Derivatives, func(i, j int) bool {
		a, c := m.Derivatives[i], m.Derivatives[j]
		if a.Kind != c.Kind {
			return a.Kind < c.Kind
		}
		if a.Key != c.Key {
			return a.Key < c.Key
		}
		return a.Digest < c.Digest
	})
}

// Verify re-checks a manifest from scratch: every derivative file must exist
// with the recorded size and digest, the source digests must be repeated
// consistently, and the recorded counts must match the derivative list. It
// also recomputes the manifest checksum.
func Verify(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.Wrap(err, fault.StageManifest, fault.KindMissing, "manifest.json", 0, "read manifest")
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fault.Wrap(err, fault.StageManifest, fault.KindCorrupt, "manifest.json", 0, "decode manifest")
	}
	m := &Manifest{
		Version: doc.Version, Sources: doc.Sources, Tracks: doc.Tracks,
		Damage: doc.Damage, TimeRanges: doc.TimeRanges, Derivatives: doc.Derivatives,
		Counts: doc.Counts,
	}
	sortManifest(m)
	normalise(m)
	want, err := m.Checksum()
	if err != nil {
		return nil, err
	}
	if want != doc.Checksum {
		return nil, fault.New(fault.StageManifest, fault.KindDigest, "manifest.json", 0,
			"manifest checksum %s does not match recomputed %s", doc.Checksum, want)
	}
	if err := verifyDerivatives(dir, m); err != nil {
		return nil, err
	}
	if err := verifyCounts(m); err != nil {
		return nil, err
	}
	if err := verifySources(m); err != nil {
		return nil, err
	}
	return m, nil
}

func verifyDerivatives(dir string, m *Manifest) error {
	byDigest := map[string]string{}
	for i, d := range m.Derivatives {
		if len(d.Digest) != 64 {
			return fault.New(fault.StageManifest, fault.KindDigest, "manifest.json", int64(i), "derivative %q has invalid digest %q", d.Kind, d.Digest)
		}
		if d.Key != Key(d.Kind, d.Layer, d.Deps) {
			return fault.New(fault.StageManifest, fault.KindDigest, d.Path, int64(i), "derivative %q key does not match its dependencies", d.Kind)
		}
		if prev, ok := byDigest[d.Digest]; ok {
			if prev != d.Path {
				return fault.New(fault.StageManifest, fault.KindConflict, d.Path, int64(i), "digest %s stored at two paths", d.Digest)
			}
			continue
		}
		byDigest[d.Digest] = d.Path
		full, err := safeJoin(dir, d.Path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return fault.Wrap(err, fault.StageManifest, fault.KindMissing, d.Path, 0, "read derivative")
		}
		if int64(len(content)) != d.Size {
			return fault.New(fault.StageManifest, fault.KindDigest, d.Path, 0, "derivative size %d does not match recorded %d", len(content), d.Size)
		}
		sum := sha256.Sum256(content)
		if got := hex.EncodeToString(sum[:]); got != d.Digest {
			return fault.New(fault.StageManifest, fault.KindDigest, d.Path, 0, "derivative digest %s does not match recorded %s", got, d.Digest)
		}
	}
	return nil
}

func verifyCounts(m *Manifest) error {
	total := m.Counts.Added + m.Counts.Reused + m.Counts.Changed
	if total != len(m.Derivatives) {
		return fault.New(fault.StageManifest, fault.KindConflict, "manifest.json", 0,
			"counts add up to %d but the manifest lists %d derivatives", total, len(m.Derivatives))
	}
	for name, n := range map[string]int{"added": m.Counts.Added, "reused": m.Counts.Reused, "changed": m.Counts.Changed, "invalidated": m.Counts.Invalidated} {
		if n < 0 {
			return fault.New(fault.StageManifest, fault.KindConflict, "manifest.json", 0, "negative %s count %d", name, n)
		}
	}
	return nil
}

func verifySources(m *Manifest) error {
	seen := map[string]bool{}
	for _, s := range m.Sources {
		if len(s.Digest) != 64 {
			return fault.New(fault.StageManifest, fault.KindDigest, s.Name, 0, "source digest is not SHA-256")
		}
		if seen[s.Digest] {
			return fault.New(fault.StageManifest, fault.KindConflict, s.Name, 0, "source digest %s listed twice", s.Digest)
		}
		seen[s.Digest] = true
		if s.Size < 0 {
			return fault.New(fault.StageManifest, fault.KindInvalid, s.Name, 0, "negative source size")
		}
	}
	return nil
}

// safeJoin resolves p under dir, refusing any path that would escape it.
func safeJoin(dir, p string) (string, error) {
	if filepath.IsAbs(p) {
		return "", fault.New(fault.StageManifest, fault.KindBoundary, p, 0, "absolute derivative path")
	}
	full := filepath.Join(dir, filepath.FromSlash(p))
	if !asset.Within(dir, full) {
		return "", fault.New(fault.StageManifest, fault.KindBoundary, p, 0, "derivative path escapes the output root")
	}
	return full, nil
}

// Summary renders one human-readable manifest line.
func (m *Manifest) Summary() string {
	sum, err := m.Checksum()
	if err != nil {
		sum = "unavailable"
	}
	return fmt.Sprintf("manifest sources=%d tracks=%d damage=%d ranges=%d derivatives=%d checksum=%s",
		len(m.Sources), len(m.Tracks), len(m.Damage), len(m.TimeRanges), len(m.Derivatives), sum)
}
