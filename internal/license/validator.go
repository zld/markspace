// Package license issues and validates locally signed segment licenses
// using an ephemeral in-process Ed25519 key pair. No real authorization
// service is contacted.
package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"mediaseal/internal/media"
)

const licenseLifetime = 10 * time.Minute

// Issuer signs and validates licenses with an ephemeral key pair that lives
// only for the duration of the process.
type Issuer struct {
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
	clock media.Clock
}

// NewIssuer generates the ephemeral Ed25519 key pair.
func NewIssuer(clock media.Clock) (*Issuer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate license key pair: %w", err)
	}
	return &Issuer{pub: pub, priv: priv, clock: clock}, nil
}

// Issue signs a license for one segment, valid from now for the license
// lifetime.
func (i *Issuer) Issue(seg media.Segment, keyID string) (media.License, error) {
	return i.issue(seg, keyID, i.clock.Now())
}

// IssueExpired signs a license whose validity window has already ended, to
// exercise the expired-license path without waiting for real time.
func (i *Issuer) IssueExpired(seg media.Segment, keyID string) (media.License, error) {
	return i.issue(seg, keyID, i.clock.Now().Add(-2*licenseLifetime))
}

func (i *Issuer) issue(seg media.Segment, keyID string, notBefore time.Time) (media.License, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return media.License{}, fmt.Errorf("generate license nonce: %w", err)
	}
	lic := media.License{
		AssetID:   seg.AssetID,
		Track:     seg.Track,
		Index:     seg.Index,
		KeyID:     keyID,
		NotBefore: notBefore,
		NotAfter:  notBefore.Add(licenseLifetime),
		Nonce:     hex.EncodeToString(nonce),
	}
	payload, err := signingPayload(lic)
	if err != nil {
		return media.License{}, err
	}
	lic.Signature = ed25519.Sign(i.priv, payload)
	return lic, nil
}

// Validate checks the license signature, segment binding and validity
// window, returning media.ReasonNone on success or a failure reason.
func (i *Issuer) Validate(lic media.License, seg media.Segment) string {
	payload, err := signingPayload(lic)
	if err != nil || !ed25519.Verify(i.pub, payload, lic.Signature) {
		return media.ReasonLicenseSignature
	}
	if lic.AssetID != seg.AssetID || lic.Track != seg.Track || lic.Index != seg.Index {
		return media.ReasonLicenseSegment
	}
	now := i.clock.Now()
	if now.Before(lic.NotBefore) || !now.Before(lic.NotAfter) {
		return media.ReasonLicenseExpired
	}
	return media.ReasonNone
}

// signingPayload is the canonical byte representation of the signed claims:
// asset, segment, key ID, validity window and unique nonce.
func signingPayload(lic media.License) ([]byte, error) {
	claims := struct {
		AssetID   string      `json:"asset_id"`
		Track     media.Track `json:"track"`
		Index     int         `json:"index"`
		KeyID     string      `json:"key_id"`
		NotBefore time.Time   `json:"not_before"`
		NotAfter  time.Time   `json:"not_after"`
		Nonce     string      `json:"nonce"`
	}{lic.AssetID, lic.Track, lic.Index, lic.KeyID, lic.NotBefore, lic.NotAfter, lic.Nonce}
	payload, err := json.Marshal(claims)
	if err != nil {
		return nil, fmt.Errorf("marshal license claims: %w", err)
	}
	return payload, nil
}
