package pipeline

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"mediakey/internal/asset"
	"mediakey/internal/crypto"
	"mediakey/internal/issuer"
	"mediakey/internal/license"
	"mediakey/internal/reason"
	"mediakey/internal/report"
	generator "mediakey/testdata"
)

// Config drives one packaging and validation run.
type Config struct {
	AssetID     string
	Segments    []generator.Segment
	RotateEvery int
	LicenseTTL  time.Duration
	// AdvanceClock moves the injected clock forward after the primary
	// validation to observe license and key expiry boundaries.
	AdvanceClock time.Duration
	// CorruptSegment is a segment index whose ciphertext is flipped after
	// packaging, provoking AUTH_FAILED. Negative disables it.
	CorruptSegment int
	// DropSegment is a segment index whose ciphertext is withheld from
	// validation, provoking SEGMENT_MISSING. Negative disables it.
	DropSegment int
	// ReplayCheck runs concurrent presentations of one license serial.
	ReplayCheck bool
	// ReplayAttempts is the number of concurrent presentations.
	ReplayAttempts int

	// KeyTTL bounds each key version's validity. Zero inherits LicenseTTL so
	// that advancing the clock past the license also retires the keys.
	KeyTTL time.Duration
	// MaxRotations caps rotations; zero derives a bound from SegmentCount.
	MaxRotations int

	// Clock supplies time. Required.
	Clock *Clock
	// KeyRandom, NonceRandom and SecretRandom supply entropy. Nil means
	// crypto/rand via the issuer, license and crypto packages.
	KeyRandom    io.Reader
	NonceRandom  io.Reader
	SecretRandom io.Reader
}

// PackageError reports a packaging-stage failure with a stable code.
type PackageError struct {
	Code   reason.Code
	Detail string
}

// Error implements error.
func (e *PackageError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

func packErr(code reason.Code, format string, args ...any) error {
	return &PackageError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Packaged is the in-memory result of packaging: the public manifest, the
// ciphertext for each segment, the signed license and the rotation log.
// Key material is not part of this structure.
type Packaged struct {
	Manifest    *asset.Manifest
	Ciphertexts map[int][]byte
	License     *license.License
	Rotations   []report.Rotation
	// KeyRange is the licensed key version range, recorded for reporting.
	KeyMin, KeyMax int
	// Distributor is the process-held key history used by validation. It never
	// leaves the process.
	Distributor *issuer.Distributor
	// Secret is the license signing key, held only in memory.
	Secret []byte
}

// Package encrypts every segment, rotating keys on segment boundaries, and
// issues the accompanying license.
//
// The nonce check happens before any segment is recorded: if the nonce source
// repeats a value already in the manifest, packaging aborts with
// DUPLICATE_NONCE and no existing segment is overwritten.
func Package(cfg Config) (*Packaged, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	now := cfg.Clock.Now()
	keyTTL := cfg.KeyTTL
	if keyTTL == 0 {
		keyTTL = cfg.LicenseTTL
	}
	keyRandom := cfg.KeyRandom
	if keyRandom == nil {
		keyRandom = rand.Reader
	}
	nonceRandom := cfg.NonceRandom
	if nonceRandom == nil {
		nonceRandom = rand.Reader
	}

	dist, err := issuer.New(issuer.Options{
		Now:          cfg.Clock.Now,
		Random:       keyRandom,
		KeyTTL:       keyTTL,
		MaxRotations: cfg.MaxRotations,
	})
	if err != nil {
		return nil, packErr(reason.BUILD_ERROR, "key distributor: %v", err)
	}

	m := asset.New(cfg.AssetID, cfg.RotateEvery)
	m.CreatedAt = now.UTC().Format(time.RFC3339Nano)
	ciphertexts := make(map[int][]byte, len(cfg.Segments))
	used := make(map[string]int, len(cfg.Segments))
	var rotations []report.Rotation
	prevID := dist.Current().ID

	for _, seg := range cfg.Segments {
		if seg.Index > 0 && seg.Index%cfg.RotateEvery == 0 {
			key, count, rerr := dist.Rotate()
			if rerr != nil {
				return nil, packErr(reason.BUILD_ERROR, "rotate before segment %d: %v", seg.Index, rerr)
			}
			rotations = append(rotations, report.Rotation{
				Rotation:  count,
				FromKeyID: prevID,
				ToKeyID:   key.ID,
				AtSegment: seg.Index,
			})
			prevID = key.ID
		}

		key, kerr := dist.ResolveForEncrypt(now)
		if kerr != nil {
			return nil, packErr(reason.KEY_EXPIRED, "segment %d: %v", seg.Index, kerr)
		}
		raw, nerr := crypto.NewNonce(nonceRandom, asset.NonceSize)
		if nerr != nil {
			return nil, packErr(reason.BUILD_ERROR, "segment %d nonce: %v", seg.Index, nerr)
		}
		nonce := asset.EncodeNonce(raw)
		if owner, exists := used[nonce]; exists {
			return nil, packErr(reason.DUPLICATE_NONCE,
				"segment %d nonce repeats segment %d; packaging aborted", seg.Index, owner)
		}
		used[nonce] = seg.Index

		aead, aerr := key.AEAD()
		if aerr != nil {
			return nil, packErr(reason.BUILD_ERROR, "segment %d: %v", seg.Index, aerr)
		}
		sealed, serr := crypto.Seal(aead, raw, seg.Data, cfg.AssetID, seg.Index)
		if serr != nil {
			return nil, packErr(reason.BUILD_ERROR, "segment %d seal: %v", seg.Index, serr)
		}
		if _, exists := ciphertexts[seg.Index]; exists {
			return nil, packErr(reason.SEGMENT_DUPLICATE, "segment %d already packaged", seg.Index)
		}
		ciphertexts[seg.Index] = sealed

		if aerr := m.Add(asset.Segment{
			Index:        seg.Index,
			KeyID:        key.ID,
			KeyVersion:   key.Version,
			Nonce:        nonce,
			PlainDigest:  asset.Digest(seg.Data),
			CipherDigest: asset.Digest(sealed),
			PlainSize:    len(seg.Data),
			CipherSize:   len(sealed),
		}); aerr != nil {
			return nil, packErr(reason.BUILD_ERROR, "segment %d manifest: %v", seg.Index, aerr)
		}
	}

	secret := make([]byte, 32)
	if cfg.SecretRandom != nil {
		if _, err := cfg.SecretRandom.Read(secret); err != nil {
			return nil, packErr(reason.BUILD_ERROR, "license secret: %v", err)
		}
	} else if _, err := rand.Reader.Read(secret); err != nil {
		return nil, packErr(reason.BUILD_ERROR, "license secret: %v", err)
	}
	signer, err := license.NewSigner(license.Options{
		Secret: secret,
		Now:    cfg.Clock.Now,
		Random: cfg.SecretRandom,
	})
	if err != nil {
		return nil, packErr(reason.BUILD_ERROR, "license signer: %v", err)
	}
	keyMin, keyMax := 1, dist.Rotations()+1
	lic, err := signer.Issue(cfg.AssetID, keyMin, keyMax, cfg.LicenseTTL)
	if err != nil {
		return nil, packErr(reason.BUILD_ERROR, "issue license: %v", err)
	}

	return &Packaged{
		Manifest:    m,
		Ciphertexts: ciphertexts,
		License:     lic,
		Rotations:   rotations,
		KeyMin:      keyMin,
		KeyMax:      keyMax,
		Distributor: dist,
		Secret:      secret,
	}, nil
}

func validateConfig(cfg Config) error {
	if cfg.AssetID == "" {
		return packErr(reason.INVALID_INPUT, "asset id is required")
	}
	if cfg.Clock == nil {
		return packErr(reason.INVALID_INPUT, "clock is required")
	}
	if cfg.RotateEvery < 1 {
		return packErr(reason.INVALID_INPUT, "rotate-every must be at least 1")
	}
	if cfg.LicenseTTL <= 0 {
		return packErr(reason.INVALID_INPUT, "license-ttl must be positive")
	}
	if len(cfg.Segments) == 0 {
		return packErr(reason.INVALID_INPUT, "no segments to package")
	}
	if len(cfg.Segments) > generator.MaxSegments {
		return packErr(reason.INVALID_INPUT, "segment count %d exceeds limit %d", len(cfg.Segments), generator.MaxSegments)
	}
	if cfg.AdvanceClock < 0 {
		return packErr(reason.INVALID_INPUT, "advance-clock must not be negative")
	}
	if cfg.ReplayAttempts < 0 {
		return packErr(reason.INVALID_INPUT, "replay attempts must not be negative")
	}
	if cfg.KeyTTL < 0 {
		return packErr(reason.INVALID_INPUT, "key-ttl must not be negative")
	}
	return nil
}
