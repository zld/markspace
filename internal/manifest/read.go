package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"mediapipe/internal/model"
)

// Read parses a JSON version manifest and validates it thoroughly enough that
// later stages can assume structural invariants hold.
func Read(r io.Reader) (*Manifest, error) {
	// A one-hour asset with three tracks and per-cell content digests stays
	// well under this; the limit only rejects runaway or hostile input.
	const maxManifestBytes = 64 << 20
	data, err := io.ReadAll(io.LimitReader(r, maxManifestBytes+1))
	if err != nil {
		return nil, model.New(model.KindInternal, model.StageManifest, "", "read", err)
	}
	if len(data) > maxManifestBytes {
		return nil, model.New(model.KindInvalid, model.StageManifest, "", "read",
			errors.New("manifest exceeds 64 MiB limit"))
	}
	return Parse(data)
}

// Parse decodes and validates manifest bytes. Unknown fields are rejected so a
// typo in a generated manifest fails loudly instead of silently dropping data.
func Parse(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	m := &Manifest{}
	if err := dec.Decode(m); err != nil {
		return nil, model.New(model.KindInvalid, model.StageManifest, "", "decode", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, model.New(model.KindInvalid, model.StageManifest, "", "decode",
			errors.New("trailing data after manifest"))
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Validate checks every invariant the merge stage relies on.
func (m *Manifest) Validate() error {
	if len(m.Assets) == 0 {
		return model.New(model.KindInvalid, model.StageManifest, "", "validate",
			errors.New("manifest has no assets"))
	}
	seenAsset := make(map[string]bool, len(m.Assets))
	for i := range m.Assets {
		a := &m.Assets[i]
		if a.Name == "" {
			return model.New(model.KindInvalid, model.StageManifest, "", "validate",
				fmt.Errorf("asset %d has empty name", i))
		}
		if seenAsset[a.Name] {
			return model.New(model.KindInvalid, model.StageManifest, a.Name, "validate",
				errors.New("duplicate asset name"))
		}
		seenAsset[a.Name] = true
		if len(a.Versions) == 0 {
			return model.New(model.KindInvalid, model.StageManifest, a.Name, "validate",
				errors.New("asset has no versions"))
		}
		if err := a.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (a *Asset) validate() error {
	seenVersion := make(map[string]bool, len(a.Versions))
	for i := range a.Versions {
		v := &a.Versions[i]
		if v.ID == "" {
			return model.New(model.KindInvalid, model.StageManifest, a.Name, "validate",
				fmt.Errorf("version %d has empty id", i))
		}
		if seenVersion[v.ID] {
			return model.New(model.KindInvalid, model.StageManifest, a.Name, "validate",
				fmt.Errorf("duplicate version id %q", v.ID))
		}
		seenVersion[v.ID] = true
		if len(v.Tracks) == 0 {
			return model.New(model.KindInvalid, model.StageManifest, a.Name, "validate",
				fmt.Errorf("version %q has no tracks", v.ID))
		}
		seenTrack := make(map[string]bool, len(v.Tracks))
		for j := range v.Tracks {
			t := &v.Tracks[j]
			if t.ID == "" || t.Kind == "" {
				return model.New(model.KindInvalid, model.StageManifest, a.Name, "validate",
					fmt.Errorf("version %q track %d has empty id or kind", v.ID, j))
			}
			if seenTrack[t.ID] {
				return model.New(model.KindInvalid, model.StageManifest, a.Name, "validate",
					fmt.Errorf("version %q duplicate track id %q", v.ID, t.ID))
			}
			seenTrack[t.ID] = true
			if err := t.validate(a.Name, v.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Track) validate(asset, version string) error {
	fail := func(msg string, args ...any) error {
		return model.New(model.KindInvalid, model.StageManifest, asset, "validate",
			fmt.Errorf(msg, args...)).WithVersion(version).WithTrack(t.ID)
	}
	if t.Timescale == 0 {
		return fail("zero timescale")
	}
	if len(t.Ranges) == 0 {
		return fail("track has no ranges")
	}
	ordered := append([]Range(nil), t.Ranges...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].StartTicks < ordered[j].StartTicks })
	for i := range ordered {
		r := ordered[i]
		if r.DurationTicks <= 0 {
			return fail("range %d has non-positive duration %d", i, r.DurationTicks)
		}
		if r.Summary == "" {
			return fail("range %d has empty summary", i)
		}
		if r.Samples < 0 || r.Bytes < 0 || r.GopTicks < 0 {
			return fail("range %d has negative counters", i)
		}
		if r.StartTicks+r.DurationTicks < r.StartTicks {
			return fail("range %d overflows int64", i)
		}
		if i > 0 {
			prev := ordered[i-1]
			if r.StartTicks < prev.EndTicks() {
				return fail("ranges %d and %d overlap on the media timeline", i-1, i)
			}
		}
	}
	return nil
}

// Canonical returns a deterministic JSON encoding of the manifest with all
// collections sorted, suitable for hashing so reruns can be compared.
func (m *Manifest) Canonical() ([]byte, error) {
	c := m.canonical()
	return json.Marshal(c)
}

func (m *Manifest) canonical() *Manifest {
	out := &Manifest{Seed: m.Seed, Window: m.Window}
	out.Assets = make([]Asset, len(m.Assets))
	for i, a := range m.Assets {
		na := Asset{Name: a.Name}
		na.Versions = append([]Version(nil), a.Versions...)
		sort.Slice(na.Versions, func(i, j int) bool { return na.Versions[i].ID < na.Versions[j].ID })
		for j := range na.Versions {
			tracks := append([]Track(nil), na.Versions[j].Tracks...)
			sort.Slice(tracks, func(x, y int) bool { return tracks[x].ID < tracks[y].ID })
			for k := range tracks {
				tracks[k].Ranges = append([]Range(nil), tracks[k].Ranges...)
			}
			na.Versions[j].Tracks = tracks
		}
		out.Assets[i] = na
	}
	sort.Slice(out.Assets, func(i, j int) bool { return out.Assets[i].Name < out.Assets[j].Name })
	return out
}
