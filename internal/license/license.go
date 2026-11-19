// Package license issues and verifies short-lived local licenses.
//
// A license binds an asset id, an allowed key version range, an issuance and
// expiry time and a unique serial. It is authenticated with HMAC-SHA256 over a
// canonical serialization of every other field, and the signature is verified
// in constant time. Licenses are only ever validated locally.
package license

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"mediakey/internal/reason"
)

// Version is the license format version.
const Version = 1

// Errors returned by this package. Each maps to a stable reason code.
var (
	ErrMissingField  = errors.New("license: missing required field")
	ErrBadSignature  = errors.New("license: signature mismatch")
	ErrAssetMismatch = errors.New("license: asset id mismatch")
	ErrExpired       = errors.New("license: expired")
	ErrMalformed     = errors.New("license: malformed field")
	ErrKeyOutOfRange = errors.New("license: key version outside licensed range")
	ErrWeakSecret    = errors.New("license: signing secret too short")
)

// License is a local, offline usage grant for one asset.
type License struct {
	Version   int    `json:"version"`
	AssetID   string `json:"asset_id"`
	KeyMin    int    `json:"key_min"`
	KeyMax    int    `json:"key_max"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	Serial    string `json:"serial"`
	// Signature is the standard-base64 HMAC-SHA256 of the canonical payload.
	Signature string `json:"signature"`
}

// payload is the signed subset of a license. Its field order defines the
// canonical bytes and must not be reordered.
type payload struct {
	Version   int    `json:"version"`
	AssetID   string `json:"asset_id"`
	KeyMin    int    `json:"key_min"`
	KeyMax    int    `json:"key_max"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	Serial    string `json:"serial"`
}

// Options configures a Signer.
type Options struct {
	// Secret is the HMAC key. At least 16 bytes are required.
	Secret []byte
	// Now supplies the clock. Required.
	Now func() time.Time
	// Random supplies serial entropy. Defaults to crypto/rand.Reader.
	Random io.Reader
}

// Signer issues licenses. The secret never leaves the process.
type Signer struct {
	secret []byte
	now    func() time.Time
	random io.Reader
}

// NewSigner validates the options and returns a signer.
func NewSigner(opts Options) (*Signer, error) {
	if len(opts.Secret) < 16 {
		return nil, ErrWeakSecret
	}
	if opts.Now == nil {
		return nil, errors.New("license: clock is required")
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	secret := make([]byte, len(opts.Secret))
	copy(secret, opts.Secret)
	return &Signer{secret: secret, now: opts.Now, random: opts.Random}, nil
}

// Issue grants assetID for validFor starting now. Every segment key version
// used for the asset must fall inside [keyMin, keyMax].
func (s *Signer) Issue(assetID string, keyMin, keyMax int, validFor time.Duration) (*License, error) {
	if assetID == "" {
		return nil, fmt.Errorf("%w: asset_id", ErrMissingField)
	}
	if keyMin < 1 || keyMax < keyMin {
		return nil, fmt.Errorf("%w: key range [%d,%d]", ErrMalformed, keyMin, keyMax)
	}
	if validFor <= 0 {
		return nil, fmt.Errorf("%w: non-positive validity", ErrMalformed)
	}
	serial, err := newSerial(s.random)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	l := &License{
		Version:   Version,
		AssetID:   assetID,
		KeyMin:    keyMin,
		KeyMax:    keyMax,
		IssuedAt:  now.Format(time.RFC3339Nano),
		ExpiresAt: now.Add(validFor).UTC().Format(time.RFC3339Nano),
		Serial:    serial,
	}
	sig, err := sign(s.secret, l.payload())
	if err != nil {
		return nil, err
	}
	l.Signature = sig
	return l, nil
}

// newSerial returns a random, process-unique identifier.
func newSerial(r io.Reader) (string, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("license: serial entropy: %w", err)
	}
	return "lic-" + hex.EncodeToString(buf), nil
}

func (l *License) payload() payload {
	return payload{
		Version:   l.Version,
		AssetID:   l.AssetID,
		KeyMin:    l.KeyMin,
		KeyMax:    l.KeyMax,
		IssuedAt:  l.IssuedAt,
		ExpiresAt: l.ExpiresAt,
		Serial:    l.Serial,
	}
}

// sign returns the standard-base64 HMAC-SHA256 of the canonical payload.
func sign(secret []byte, p payload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("license: canonical payload: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(raw)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// verifySignature recomputes the HMAC and compares it in constant time.
func verifySignature(secret []byte, l *License) error {
	want, err := sign(secret, l.payload())
	if err != nil {
		return err
	}
	got, err := base64.StdEncoding.DecodeString(l.Signature)
	if err != nil {
		return fmt.Errorf("%w: undecodable signature", ErrBadSignature)
	}
	wantRaw, err := base64.StdEncoding.DecodeString(want)
	if err != nil {
		return err
	}
	if !hmac.Equal(got, wantRaw) {
		return ErrBadSignature
	}
	return nil
}

// Check performs every validation except replay detection, in an order that
// yields the most specific reason: field presence, signature, asset and
// expiry. now is the validation time; assetID is the asset being opened.
func (l *License) Check(secret []byte, now time.Time, assetID string) error {
	if l == nil {
		return fmt.Errorf("%w: license", ErrMissingField)
	}
	if err := l.checkFields(); err != nil {
		return err
	}
	if err := verifySignature(secret, l); err != nil {
		return err
	}
	if l.AssetID != assetID {
		return fmt.Errorf("%w: license for %q, asset %q", ErrAssetMismatch, l.AssetID, assetID)
	}
	expires, err := time.Parse(time.RFC3339Nano, l.ExpiresAt)
	if err != nil {
		return fmt.Errorf("%w: expires_at", ErrMalformed)
	}
	if !now.Before(expires) {
		return fmt.Errorf("%w: expired at %s", ErrExpired, expires.UTC().Format(time.RFC3339))
	}
	return nil
}

// checkFields reports the first missing or malformed field.
func (l *License) checkFields() error {
	switch {
	case l.Version == 0:
		return fmt.Errorf("%w: version", ErrMissingField)
	case l.AssetID == "":
		return fmt.Errorf("%w: asset_id", ErrMissingField)
	case l.KeyMin == 0:
		return fmt.Errorf("%w: key_min", ErrMissingField)
	case l.KeyMax == 0:
		return fmt.Errorf("%w: key_max", ErrMissingField)
	case l.IssuedAt == "":
		return fmt.Errorf("%w: issued_at", ErrMissingField)
	case l.ExpiresAt == "":
		return fmt.Errorf("%w: expires_at", ErrMissingField)
	case l.Serial == "":
		return fmt.Errorf("%w: serial", ErrMissingField)
	case l.Signature == "":
		return fmt.Errorf("%w: signature", ErrMissingField)
	}
	if l.Version != Version {
		return fmt.Errorf("%w: unsupported version %d", ErrMalformed, l.Version)
	}
	if l.KeyMin < 1 || l.KeyMax < l.KeyMin {
		return fmt.Errorf("%w: key range [%d,%d]", ErrMalformed, l.KeyMin, l.KeyMax)
	}
	issued, err := time.Parse(time.RFC3339Nano, l.IssuedAt)
	if err != nil {
		return fmt.Errorf("%w: issued_at", ErrMalformed)
	}
	expires, err := time.Parse(time.RFC3339Nano, l.ExpiresAt)
	if err != nil {
		return fmt.Errorf("%w: expires_at", ErrMalformed)
	}
	if !expires.After(issued) {
		return fmt.Errorf("%w: expiry not after issuance", ErrMalformed)
	}
	return nil
}

// AllowsKey reports whether version v is inside the licensed range.
func (l *License) AllowsKey(v int) bool { return v >= l.KeyMin && v <= l.KeyMax }

// CheckKeyRange returns nil when v is licensed, otherwise ErrKeyOutOfRange.
func (l *License) CheckKeyRange(v int) error {
	if l == nil {
		return fmt.Errorf("%w: license", ErrMissingField)
	}
	if !l.AllowsKey(v) {
		return fmt.Errorf("%w: version %d not in [%d,%d]", ErrKeyOutOfRange, v, l.KeyMin, l.KeyMax)
	}
	return nil
}

// Encode serializes the license deterministically.
func Encode(l *License) ([]byte, error) {
	if l == nil {
		return nil, errors.New("license: nil")
	}
	return json.MarshalIndent(l, "", "  ")
}

// Decode parses a license without validating it, so that validation failures
// can be classified rather than collapsing into a parse error.
func Decode(data []byte) (*License, error) {
	var l License
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("license: decode: %w", err)
	}
	return &l, nil
}

// ReasonCode maps a validation error to its stable reason code.
func ReasonCode(err error) reason.Code {
	switch {
	case err == nil:
		return reason.NONE
	case errors.Is(err, ErrMissingField):
		return reason.LICENSE_MISSING_FIELD
	case errors.Is(err, ErrBadSignature):
		return reason.LICENSE_SIGNATURE
	case errors.Is(err, ErrAssetMismatch):
		return reason.LICENSE_ASSET_MISMATCH
	case errors.Is(err, ErrExpired):
		return reason.LICENSE_EXPIRED
	case errors.Is(err, ErrKeyOutOfRange):
		return reason.KEY_OUT_OF_RANGE
	default:
		return reason.LICENSE_INVALID
	}
}
