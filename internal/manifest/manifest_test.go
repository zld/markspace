package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"example.com/assetpack/internal/fault"
)

func digest(ch byte) string { return strings.Repeat(string(ch), 64) }

func legacyJSON() []byte {
	m := V1{Version: 1, Release: "spring", Assets: []V1Asset{
		{Name: "intro", Path: "src/intro.ts", Rate: 44100, Channels: 2, DurationMS: 2000, SHA256: digest('a')},
		{Name: "bed", Path: "src/bed.ts", Rate: 48000, Channels: 1, DurationMS: 3000, SHA256: digest('b')},
	}}
	raw, _ := json.Marshal(m)
	return raw
}

func resolvedFor(old *V1) []Resolved {
	out := make([]Resolved, 0, len(old.Assets))
	for i, a := range old.Assets {
		out = append(out, Resolved{
			Name: a.Name, SHA256: digest(byte('c' + i)), SourceSHA256: a.SHA256,
			Bytes: 4096, Artifact: a.Name + ".m4a", Frames: int64(a.DurationMS) * 48,
			LoudnessLUFS: -16, Peak: 0.5,
			Media: []Interval{{StartSample: 0, LengthSamples: int64(a.DurationMS) * 48, SourceOffset: 0}},
		})
	}
	return out
}

func TestReadV1(t *testing.T) {
	m, err := ReadV1(bytes.NewReader(legacyJSON()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || len(m.Assets) != 2 || m.Assets[1].Name != "bed" {
		t.Fatalf("bad legacy parse: %+v", m)
	}
}

func TestReadV1Rejections(t *testing.T) {
	cases := map[string][]byte{
		"unknown version": []byte(`{"version":3,"release":"x","assets":[]}`),
		"no version":      []byte(`{"release":"x","assets":[]}`),
		"bad json":        []byte(`{`),
		"no release":      []byte(`{"version":1,"assets":[]}`),
		"no assets":       []byte(`{"version":1,"release":"x","assets":[]}`),
		"missing digest":  []byte(`{"version":1,"release":"x","assets":[{"name":"a","path":"p","rate":48000,"channels":1,"duration_ms":1}]}`),
		"bad digest":      []byte(`{"version":1,"release":"x","assets":[{"name":"a","path":"p","rate":48000,"channels":1,"duration_ms":1,"sha256":"zz"}]}`),
		"duplicate name":  []byte(`{"version":1,"release":"x","assets":[{"name":"a","path":"p","rate":48000,"channels":1,"duration_ms":1,"sha256":"` + digest('a') + `"},{"name":"a","path":"q","rate":48000,"channels":1,"duration_ms":1,"sha256":"` + digest('a') + `"}]}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadV1(bytes.NewReader(raw)); err == nil {
				t.Fatalf("%s: accepted", name)
			}
		})
	}
}

func TestConvertPreservesProvenanceAndIntervals(t *testing.T) {
	old, err := ReadV1(bytes.NewReader(legacyJSON()))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	m, err := adapter.Convert(old, resolvedFor(old))
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != SchemaV2 || m.Release != "spring" || len(m.Resources) != 2 {
		t.Fatalf("bad conversion: %+v", m)
	}
	for i, r := range m.Resources {
		if r.SourceVersion != VersionLegacy || r.SourceSHA256 != old.Assets[i].SHA256 {
			t.Fatalf("provenance lost: %+v", r)
		}
		if r.Kind != "audio" || len(r.Media) != 1 || r.Media[0].LengthSamples != r.Frames {
			t.Fatalf("interval lost: %+v", r)
		}
		if r.Rate != 48000 || r.Layout != "stereo" {
			t.Fatalf("target not recorded: %+v", r)
		}
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("converted manifest invalid: %v", err)
	}
}

func TestConvertRejectsDigestMismatch(t *testing.T) {
	old, _ := ReadV1(bytes.NewReader(legacyJSON()))
	res := resolvedFor(old)
	res[0].SourceSHA256 = digest('f')
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	_, err := adapter.Convert(old, res)
	var f *fault.Fault
	if !errors.As(err, &f) || f.Kind != fault.KindDigest {
		t.Fatalf("expected digest fault, got %v", err)
	}
}

func TestConvertRejectsMissingOutput(t *testing.T) {
	old, _ := ReadV1(bytes.NewReader(legacyJSON()))
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	if _, err := adapter.Convert(old, resolvedFor(old)[:1]); err == nil {
		t.Fatal("missing output accepted")
	}
}

func TestV2DigestStableAcrossOrdering(t *testing.T) {
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	old, _ := ReadV1(bytes.NewReader(legacyJSON()))
	m, err := adapter.Convert(old, resolvedFor(old))
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Shuffle resource order and re-encode: canonical bytes must be identical.
	m2 := m.Clone()
	m2.Resources[0], m2.Resources[1] = m2.Resources[1], m2.Resources[0]
	m2.Digest = ""
	second, err := m2.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("canonical encoding depends on resource order")
	}
	// Round trip verifies the digest.
	back, err := ReadV2(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if back.Digest != m.Digest {
		t.Fatal("digest changed on round trip")
	}
}

func TestV2RejectsTamperedDigest(t *testing.T) {
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	old, _ := ReadV1(bytes.NewReader(legacyJSON()))
	m, _ := adapter.Convert(old, resolvedFor(old))
	raw, _ := m.Encode()
	var generic map[string]interface{}
	_ = json.Unmarshal(raw, &generic)
	generic["release"] = "tampered"
	tampered, _ := json.Marshal(generic)
	_, err := ReadV2(bytes.NewReader(tampered))
	var f *fault.Fault
	if !errors.As(err, &f) || f.Kind != fault.KindDigest {
		t.Fatalf("tampered manifest accepted: %v", err)
	}
}

func TestV2RejectsUnknownSchemaAndMissingFields(t *testing.T) {
	cases := []string{
		`{"schema":"assetpack/v9","release":"r"}`,
		`{"schema":"assetpack/v2"}`,
		`{"version":1,"release":"r","assets":[]}`,
		`{"release":"r"}`,
	}
	for _, raw := range cases {
		if _, err := ReadV2(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestVerifyDigests(t *testing.T) {
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	old, _ := ReadV1(bytes.NewReader(legacyJSON()))
	m, _ := adapter.Convert(old, resolvedFor(old))
	ok := map[string]string{}
	for _, r := range m.Resources {
		ok[r.Artifact] = r.SHA256
	}
	if err := m.Verify(ok); err != nil {
		t.Fatalf("valid artifacts rejected: %v", err)
	}
	bad := map[string]string{}
	for k, v := range ok {
		bad[k] = v
	}
	for k := range bad {
		bad[k] = digest('0')
		break
	}
	if err := m.Verify(bad); err == nil {
		t.Fatal("mismatched artifact accepted")
	}
	if err := m.Verify(map[string]string{}); err == nil {
		t.Fatal("missing artifacts accepted")
	}
}

func TestAdapterReadFileDispatch(t *testing.T) {
	a := &Adapter{}
	legacy, cur, err := a.ReadFile(bytes.NewReader(legacyJSON()))
	if err != nil || legacy == nil || cur != nil {
		t.Fatalf("v1 dispatch failed: %v", err)
	}
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	m, _ := adapter.Convert(legacy, resolvedFor(legacy))
	raw, _ := m.Encode()
	_, cur2, err := a.ReadFile(bytes.NewReader(raw))
	if err != nil || cur2 == nil {
		t.Fatalf("v2 dispatch failed: %v", err)
	}
	if _, _, err := a.ReadFile(strings.NewReader(`{"version":7}`)); err == nil {
		t.Fatal("unknown version accepted")
	}
	if _, _, err := a.ReadFile(strings.NewReader("   ")); err == nil {
		t.Fatal("empty manifest accepted")
	}
}

func TestValidateResourceIntervals(t *testing.T) {
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	old, _ := ReadV1(bytes.NewReader(legacyJSON()))
	m, _ := adapter.Convert(old, resolvedFor(old))
	m.Resources[0].Frames += 10
	m.Digest = ""
	if _, err := m.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err == nil {
		t.Fatal("interval coverage mismatch accepted")
	}
}
