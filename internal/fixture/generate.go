// Package fixture produces deterministic sample media and fault scenarios.
// Everything is generated in-process from fixed seeds so demos are
// reproducible; no network or source media files are required.
package fixture

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"

	"mediakey/internal/manifest"
)

// Fixed seeds and timestamps. They are test material only and must never be
// used as production keys.
const (
	RootSeedText    = "mediakey-demo-root-seed-v1"
	LicenseSeedText = "mediakey-demo-license-seed-v1"
	// BaseTime is a fixed instant (2026-01-01T00:00:00Z) so all time-based
	// behavior is reproducible across machines.
	BaseTime int64 = 1767225600
)

// Injection names accepted on the command line (comma-separated).
const (
	InjectExpired = "expired"
	InjectReplay  = "replay"
	InjectTamper  = "tamper"
	InjectWindow  = "out-of-window"
)

// Plan describes how a scenario should be built.
type Plan struct {
	AssetID      string
	Chunks       int
	ChunkSize    int
	RotateEvery  int
	GraceSeconds int64
	Injections   map[string]bool
}

// Scenario is a fully generated, ready-to-process fixture.
type Scenario struct {
	Plan          Plan
	RootKey       []byte
	SigningKey    []byte
	Plaintext     []byte
	ChunkRounds   []int
	Now           int64
	LicenseExpiry int64
	// Per-chunk injected faults; absent means a healthy chunk.
	Faults map[int]string
}

// chunkContent generates deterministic, distinct bytes for chunk idx. HMAC
// output is keyed off the asset id and index so each chunk differs while the
// whole asset stays reproducible.
func chunkContent(assetID string, idx, size int) []byte {
	out := make([]byte, 0, size)
	block := 0
	for len(out) < size {
		mac := hmac.New(sha256.New, []byte(RootSeedText+":content"))
		mac.Write([]byte(assetID))
		var b [8]byte
		binary.BigEndian.PutUint32(b[:4], uint32(idx))
		binary.BigEndian.PutUint32(b[4:], uint32(block))
		mac.Write(b[:])
		out = append(out, mac.Sum(nil)...)
		block++
	}
	return out[:size]
}

// New builds plaintext, per-chunk round assignments, clocks and fault
// placements. Licenses and envelopes are created later by the pipeline, which
// owns the crypto orchestration.
func New(p Plan) (*Scenario, error) {
	if p.Chunks <= 0 || p.Chunks > manifest.MaxChunks {
		return nil, manifest.NewError(manifest.ReasonLimitExceeded, "chunk count out of range")
	}
	if p.ChunkSize <= 0 || p.ChunkSize > manifest.MaxChunkBytes {
		return nil, manifest.NewError(manifest.ReasonLimitExceeded, "chunk size out of range")
	}
	if p.RotateEvery <= 0 {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "rotate-every must be positive")
	}
	if p.AssetID == "" {
		p.AssetID = "demo-asset-0001"
	}
	sc := &Scenario{
		Plan:        p,
		RootKey:     deriveKey(RootSeedText),
		SigningKey:  deriveKey(LicenseSeedText),
		ChunkRounds: make([]int, p.Chunks),
		Faults:      map[int]string{},
	}
	// Deterministic plaintext.
	for i := 0; i < p.Chunks; i++ {
		sc.Plaintext = append(sc.Plaintext, chunkContent(p.AssetID, i, p.ChunkSize)...)
		sc.ChunkRounds[i] = i / p.RotateEvery
	}
	sc.Now = BaseTime + 3600
	// License is valid for one hour from base; demo "now" sits just after it.
	sc.LicenseExpiry = BaseTime + 1800
	return sc, nil
}

// PlaceFaults assigns each requested injection to a distinct, predictable
// chunk so the demo always exhibits every category at least once. Healthy
// chunks are left untouched.
func (s *Scenario) PlaceFaults() {
	n := s.Plan.Chunks
	pick := func(step, offset int) int { return (step*3 + offset) % n }
	for name := range s.Plan.Injections {
		var idx int
		switch name {
		case InjectExpired:
			idx = pick(0, 1)
		case InjectReplay:
			idx = pick(1, 2)
		case InjectTamper:
			idx = pick(2, 0)
		case InjectWindow:
			idx = pick(3, 1)
		default:
			continue
		}
		// Never overwrite an earlier injection; move forward until free.
		for s.Faults[idx] != "" {
			idx = (idx + 1) % n
		}
		s.Faults[idx] = name
	}
}

func deriveKey(label string) []byte {
	mac := hmac.New(sha256.New, []byte(label+":seed"))
	mac.Write([]byte("mediakey/fixture/v1"))
	return mac.Sum(nil)
}
