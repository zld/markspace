package pipeline

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"mediakey/internal/crypto"
	"mediakey/internal/fixture"
	"mediakey/internal/keys"
	"mediakey/internal/license"
	"mediakey/internal/manifest"
	"mediakey/internal/replay"
)

// fixtureRoot derives the standard in-memory test root key for an asset.
func fixtureRoot(assetID string) []byte {
	return keys.DeriveTestRootKey(append([]byte(fixture.RootSeedText), []byte(":"+assetID)...))
}

// Verify runs every chunk through replay protection, license checks, key
// dispatch and AES-GCM open, returning one result per chunk. Worker count > 1
// processes chunks concurrently; shared state is mutex/guarded inside its own
// module so concurrent nonce races can have exactly one winner.
func Verify(cfg VerifyConfig) ([]manifest.ChunkResult, int, error) {
	if err := cfg.Manifest.Validate(); err != nil {
		return nil, 0, err
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	disp, err := keys.New(cfg.RootKey, maxRotate(cfg))
	if err != nil {
		return nil, 0, err
	}
	defer disp.Close()

	cacheWindow := cfg.CacheWindow
	if cacheWindow <= 0 {
		cacheWindow = 60 * time.Second
	}
	capacity := cfg.CacheCap
	if capacity <= 0 {
		capacity = len(cfg.Manifest.Chunks) * 2
	}
	cache, err := replay.New(capacity, cacheWindow)
	if err != nil {
		return nil, 0, err
	}
	rt := &Runtime{
		dispatcher: disp,
		cache:      cache,
		lic:        cfg.Lic,
		signingKey: cfg.SigningKey,
		now:        func() time.Time { return cfg.Now },
		grace:      cfg.GraceWindow,
		nonceFor:   cfg.NonceFor,
		faults:     cfg.Faults,
		seq:        0,
	}

	n := len(cfg.Manifest.Chunks)
	results := make([]manifest.ChunkResult, n)
	sem := make(chan struct{}, cfg.Workers)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = rt.processChunk(cfg.Manifest, idx)
		}(i)
	}
	wg.Wait()
	return results, disp.RotationCount(), nil
}

func maxRotate(cfg VerifyConfig) int {
	// Rotation interval is recovered from the largest gap between rounds.
	every := 1
	for _, c := range cfg.Manifest.Chunks {
		if c.Round > 0 {
			every = c.Index / c.Round
			break
		}
	}
	if every <= 0 {
		every = 1
	}
	return every
}

// processChunk executes the full per-chunk verification chain. It always
// returns a populated ChunkResult whose KeyID identifies the key the chunk
// claims, even when verification fails early.
func (rt *Runtime) processChunk(m *manifest.Manifest, idx int) manifest.ChunkResult {
	chunk := m.Chunks[idx]
	env := m.Envelopes[idx]
	res := manifest.ChunkResult{ChunkIndex: idx, KeyID: env.KeyID, Status: manifest.StatusOK}
	fault := rt.faults[idx]

	// 1. Structural attack classes detectable before crypto work.
	switch fault {
	case "truncated":
		return fail(res, manifest.ReasonTruncated, "injected truncated envelope")
	case "reordered":
		return fail(res, manifest.ReasonChunkReordered, "injected out-of-order chunk")
	case "duplicate":
		return fail(res, manifest.ReasonChunkDuplicate, "injected duplicate chunk")
	}
	if tampered := fault == fixture.InjectTamper; tampered {
		fixture.TamperEnvelope(&env)
	}

	// 2. Replay protection: unique nonce + monotonic sequence, shared cache.
	nonce, seq := rt.nextRequest(idx, fault)
	if err := rt.cache.Check(nonce, seq, rt.now()); err != nil {
		return fail(res, manifest.ErrorCode(err), err.Error())
	}

	// 3. Rotate/activate rounds before touching keys, so rotation counts
	// stay consistent even when a later check rejects the chunk.
	round, err := rt.dispatcher.EnsureForIndex(idx)
	if err != nil {
		return fail(res, manifest.ErrorCode(err), err.Error())
	}
	if round != chunk.Round {
		return fail(res, manifest.ReasonChunkReordered, "manifest round disagrees with key schedule")
	}

	// 4. License scope and time window. The "expired" injection exercises
	// strict verification (no grace), while "out-of-window" shifts the clock
	// past the overlap window so grace itself reports key_expired.
	now := rt.now()
	if fault == fixture.InjectWindow {
		now = now.Add(rt.grace + time.Second)
	}
	if rt.lic != nil {
		var lerr error
		if rt.grace > 0 && fault != fixture.InjectExpired {
			lerr = rt.lic.ExpiredWithGrace(rt.signingKey, m.Asset.ID, round, idx, now)
		} else {
			lerr = rt.lic.Verify(rt.signingKey, m.Asset.ID, round, idx, now)
		}
		if lerr != nil {
			return fail(res, manifest.ErrorCode(lerr), lerr.Error())
		}
	}

	// 5. Key dispatch and authenticated decryption.
	key, keyID, err := rt.dispatcher.KeyForRound(round)
	if err != nil {
		return fail(res, manifest.ErrorCode(err), err.Error())
	}
	res.KeyID = keyID
	if _, err := crypto.Decrypt(key, &env); err != nil {
		return fail(res, manifest.ErrorCode(err), err.Error())
	}
	return res
}

// nextRequest produces the nonce and sequence for a chunk request. Replay
// injection reuses another chunk's nonce so the shared cache rejects it.
func (rt *Runtime) nextRequest(idx int, fault string) (string, int64) {
	rt.seqMu.Lock()
	defer rt.seqMu.Unlock()
	rt.seq++
	seq := rt.seq
	var nonce string
	switch {
	case rt.nonceFor != nil:
		nonce = rt.nonceFor(idx)
	default:
		var b [16]byte
		_, _ = rand.Read(b[:])
		nonce = hex.EncodeToString(b[:])
	}
	if fault == fixture.InjectReplay && idx > 0 {
		nonce = rt.nonceFor(idx - 1)
	}
	return nonce, seq
}

func fail(r manifest.ChunkResult, code manifest.FailureReason, detail string) manifest.ChunkResult {
	r.Status = manifest.StatusFailed
	r.FailureReason = code
	r.Detail = detail
	return r
}

// SignLicense is a thin helper used by the CLI to sign freshly built licenses.
func SignLicense(l *license.License, signingKey []byte) error { return l.Sign(signingKey) }
