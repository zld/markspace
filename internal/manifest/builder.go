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
)

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
