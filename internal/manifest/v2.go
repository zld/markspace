package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strings"

	"example.com/assetpack/internal/fault"
)

// SchemaV2 is the manifest schema identifier written by this tool.
const SchemaV2 = "assetpack/v2"

// Interval is a media range expressed on the output sample clock, tied back
// to the source byte offset that produced it.
type Interval struct {
	StartSample   int64 `json:"start_sample"`
	LengthSamples int64 `json:"length_samples"`
	SourceOffset  int64 `json:"source_offset"`
}

// Resource is one released audio asset.
type Resource struct {
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	SourceVersion int        `json:"source_version"`
	SourceSHA256  string     `json:"source_sha256"`
	SHA256        string     `json:"sha256"`
	Bytes         int64      `json:"bytes"`
	Artifact      string     `json:"artifact"`
	Rate          int        `json:"rate"`
	Layout        string     `json:"layout"`
	Frames        int64      `json:"frames"`
	LoudnessLUFS  float64    `json:"loudness_lufs"`
	Peak          float64    `json:"peak"`
	GapSamples    int64      `json:"gap_samples"`
	Media         []Interval `json:"media"`
}

// V2 is the release manifest.
type V2 struct {
	Schema    string     `json:"schema"`
	Release   string     `json:"release"`
	Sequence  int64      `json:"sequence"`
	Target    TargetSpec `json:"target"`
	Resources []Resource `json:"resources"`
	// Digest is the SHA-256 of the canonical encoding of every other field.
	Digest string `json:"digest"`
}

// TargetSpec is the distribution target recorded in the manifest.
type TargetSpec struct {
	Rate   int     `json:"rate"`
	Layout string  `json:"layout"`
	LUFS   float64 `json:"lufs"`
}

var artifactName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Canonical renders v2 excluding the Digest field, with resources and their
// intervals in a deterministic order, and no HTML escaping or trailing
// newline.  The same content always produces the same bytes.
func (m *V2) Canonical() ([]byte, error) {
	clone := *m
	clone.Digest = ""
	clone.Resources = append([]Resource(nil), m.Resources...)
	sort.Slice(clone.Resources, func(i, j int) bool { return clone.Resources[i].Name < clone.Resources[j].Name })
	for i := range clone.Resources {
		iv := append([]Interval(nil), clone.Resources[i].Media...)
		sort.Slice(iv, func(a, b int) bool {
			if iv[a].StartSample != iv[b].StartSample {
				return iv[a].StartSample < iv[b].StartSample
			}
			return iv[a].SourceOffset < iv[b].SourceOffset
		})
		clone.Resources[i].Media = iv
	}
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&clone); err != nil {
		return nil, fault.New(fault.StageManifest, fault.KindIO, "encode manifest: %v", err)
	}
	return []byte(strings.TrimRight(sb.String(), "\n")), nil
}

// ComputeDigest recalculates and stores the manifest digest.
func (m *V2) ComputeDigest() (string, error) {
	body, err := m.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	m.Digest = hex.EncodeToString(sum[:])
	return m.Digest, nil
}

// Encode returns canonical bytes with the digest field populated.
func (m *V2) Encode() ([]byte, error) {
	if _, err := m.ComputeDigest(); err != nil {
		return nil, err
	}
	clone := *m
	clone.Resources = append([]Resource(nil), m.Resources...)
	sort.Slice(clone.Resources, func(i, j int) bool { return clone.Resources[i].Name < clone.Resources[j].Name })
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&clone); err != nil {
		return nil, fault.New(fault.StageManifest, fault.KindIO, "encode manifest: %v", err)
	}
	return []byte(sb.String()), nil
}

// ReadV2 parses a manifest and validates its schema, fields and digest.
func ReadV2(r io.Reader) (*V2, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 32<<20))
	if err != nil {
		return nil, fault.New(fault.StageManifest, fault.KindIO, "read manifest: %v", err)
	}
	var probe struct {
		Schema  *string `json:"schema"`
		Version *int    `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fault.New(fault.StageManifest, fault.KindMissing, "manifest is not valid JSON: %v", err)
	}
	if probe.Schema == nil {
		if probe.Version != nil {
			return nil, fault.New(fault.StageManifest, fault.KindUnknownVer,
				"manifest declares legacy version %d, not schema %q", *probe.Version, SchemaV2)
		}
		return nil, fault.New(fault.StageManifest, fault.KindMissing, "manifest has no schema field")
	}
	if *probe.Schema != SchemaV2 {
		return nil, fault.New(fault.StageManifest, fault.KindUnknownVer,
			"unknown manifest schema %q (want %q)", *probe.Schema, SchemaV2)
	}
	var m V2
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fault.New(fault.StageManifest, fault.KindMissing, "manifest shape invalid: %v", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks structure and verifies the stored digest.
func (m *V2) Validate() error {
	if m.Schema != SchemaV2 {
		return fault.New(fault.StageManifest, fault.KindUnknownVer, "unsupported schema %q", m.Schema)
	}
	if m.Release == "" {
		return fault.New(fault.StageManifest, fault.KindMissing, "manifest has no release name")
	}
	if m.Target.Rate <= 0 || m.Target.Layout == "" {
		return fault.New(fault.StageManifest, fault.KindMissing, "manifest target is incomplete")
	}
	if m.Target.LUFS >= 0 {
		return fault.New(fault.StageManifest, fault.KindInvalidArg,
			"manifest loudness target %.1f is not a negative LUFS value", m.Target.LUFS)
	}
	seen := map[string]bool{}
	for i, r := range m.Resources {
		if err := validateResource(r); err != nil {
			return err
		}
		if seen[r.Name] {
			return fault.New(fault.StageManifest, fault.KindInvalidArg,
				"resource %q appears twice", r.Name).AtSample(int64(i))
		}
		seen[r.Name] = true
	}
	if !hexDigest.MatchString(m.Digest) {
		return fault.New(fault.StageManifest, fault.KindMissing,
			"manifest digest %q is not a sha256 hex string", m.Digest)
	}
	stored := m.Digest
	got, err := m.ComputeDigest()
	if err != nil {
		return err
	}
	if got != stored {
		m.Digest = stored
		return fault.New(fault.StageManifest, fault.KindDigest,
			"manifest digest mismatch: stored %s, recomputed %s", stored, got)
	}
	return nil
}

func validateResource(r Resource) error {
	fail := func(kind fault.Kind, format string, args ...interface{}) error {
		return fault.New(fault.StageManifest, kind, format, args...)
	}
	if r.Name == "" {
		return fail(fault.KindMissing, "a resource has no name")
	}
	if !artifactName.MatchString(r.Artifact) {
		return fail(fault.KindPath, "resource %q artifact %q is not a plain file name", r.Name, r.Artifact)
	}
	if r.Kind == "" {
		return fail(fault.KindMissing, "resource %q has no kind", r.Name)
	}
	if r.SourceVersion <= 0 {
		return fail(fault.KindMissing, "resource %q has no source version", r.Name)
	}
	if !hexDigest.MatchString(r.SourceSHA256) {
		return fail(fault.KindMissing, "resource %q source digest malformed", r.Name)
	}
	if !hexDigest.MatchString(r.SHA256) {
		return fail(fault.KindMissing, "resource %q digest malformed", r.Name)
	}
	if r.Bytes <= 0 {
		return fail(fault.KindMissing, "resource %q has no byte count", r.Name)
	}
	if r.Rate <= 0 || r.Layout == "" || r.Frames <= 0 {
		return fail(fault.KindMissing, "resource %q media description incomplete", r.Name)
	}
	if len(r.Media) == 0 {
		return fail(fault.KindMissing, "resource %q has no media intervals", r.Name)
	}
	var covered int64
	for _, iv := range r.Media {
		if iv.StartSample < 0 || iv.LengthSamples <= 0 {
			return fail(fault.KindInvalidArg, "resource %q has an invalid interval", r.Name)
		}
		covered += iv.LengthSamples
	}
	if covered+r.GapSamples != r.Frames {
		return fail(fault.KindInvalidArg,
			"resource %q intervals cover %d samples and %d are declared gaps, but the resource declares %d",
			r.Name, covered, r.GapSamples, r.Frames)
	}
	if r.Peak > 1.0+1e-9 || r.Peak < 0 {
		return fail(fault.KindInvalidArg, "resource %q peak %f outside [0,1]", r.Name, r.Peak)
	}
	return nil
}

// Lookup returns a resource by name.
func (m *V2) Lookup(name string) (Resource, bool) {
	for _, r := range m.Resources {
		if r.Name == name {
			return r, true
		}
	}
	return Resource{}, false
}

// Names returns resource names in manifest order.
func (m *V2) Names() []string {
	out := make([]string, 0, len(m.Resources))
	for _, r := range m.Resources {
		out = append(out, r.Name)
	}
	return out
}

// Clone returns a deep copy safe to mutate before re-encoding.
func (m *V2) Clone() *V2 {
	out := *m
	out.Resources = make([]Resource, len(m.Resources))
	copy(out.Resources, m.Resources)
	for i := range out.Resources {
		out.Resources[i].Media = append([]Interval(nil), m.Resources[i].Media...)
	}
	return &out
}
