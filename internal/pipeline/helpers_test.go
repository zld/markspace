package pipeline_test

import (
	"testing"
	"time"

	"mediakey/internal/fixture"
	"mediakey/internal/license"
	"mediakey/internal/manifest"
	"mediakey/internal/pipeline"
)

// testLicense builds and signs a license covering every round and chunk in m,
// valid from one minute before ref until one hour after it.
func testLicense(m *manifest.Manifest, sc *fixture.Scenario, ref time.Time) *license.License {
	maxRound := 0
	for _, c := range m.Chunks {
		if c.Round > maxRound {
			maxRound = c.Round
		}
	}
	lic := &license.License{
		AssetID:  m.Asset.ID,
		MinRound: 0,
		MaxRound: maxRound,
		MinChunk: 0,
		MaxChunk: len(m.Chunks) - 1,
		NotBefore: ref.Unix() - 60,
		NotAfter:  ref.Unix() + 3600,
	}
	if err := lic.Sign(sc.SigningKey); err != nil {
		panic(err)
	}
	return lic
}

// buildHealthy produces an encrypted manifest plus verification inputs with
// no fault injections, shared by the cross-module tests.
func buildHealthy(t *testing.T, chunks, size, rotate int) (pipeline.VerifyConfig, *manifest.Manifest) {
	t.Helper()
	sc, err := fixture.New(fixture.Plan{
		AssetID:     "test-asset",
		Chunks:      chunks,
		ChunkSize:   size,
		RotateEvery: rotate,
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	m, err := pipeline.Encrypt(pipeline.EncryptConfig{
		AssetID: "test-asset", ChunkSize: size,
		RotateEvery: rotate, Plaintext: sc.Plaintext, RootKey: sc.RootKey,
	})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	now := time.Unix(sc.Now, 0).UTC()
	lic := testLicense(m, sc, now)
	cfg := pipeline.VerifyConfig{
		Manifest:   m,
		RootKey:    sc.RootKey,
		SigningKey: sc.SigningKey,
		Lic:        lic,
		Now:        now,
		Workers:    1,
		NonceFor:   func(i int) string { return fixture.NonceFor("test-asset", i) },
	}
	return cfg, m
}
