package pipeline

import (
	"errors"
	"fmt"
	"sort"

	"mediakey/internal/asset"
	"mediakey/internal/crypto"
	"mediakey/internal/issuer"
	"mediakey/internal/reason"
	"mediakey/internal/report"
)

// segmentResults walks every expected index once and classifies it without
// short-circuiting.
func segmentResults(m *asset.Manifest, delivered map[int][]byte, opts ValidateOptions) []report.SegmentResult {
	byIndex := make(map[int]asset.Segment, len(m.Segments))
	duplicates := make(map[int]int, 0)
	outOfOrder := make(map[int]bool)
	prev := -1
	for pos, seg := range m.Segments {
		if _, seen := byIndex[seg.Index]; seen {
			duplicates[seg.Index]++
			continue
		}
		byIndex[seg.Index] = seg
		if seg.Index != pos || seg.Index <= prev {
			outOfOrder[seg.Index] = true
		}
		if seg.Index > prev {
			prev = seg.Index
		}
	}

	// The walk covers only indices the manifest claims: the declared count and
	// every listed index. Bytes delivered under any other index are reported
	// as SEGMENT_UNEXPECTED rather than inflating the walk with phantom gaps.
	maxIndex := maxInt(m.SegmentCount-1, maxKey(byIndex))
	out := make([]report.SegmentResult, 0, maxIndex+1)
	for idx := 0; idx <= maxIndex; idx++ {
		seg, inManifest := byIndex[idx]
		if count, dup := duplicates[idx]; dup {
			out = append(out, report.SegmentResult{
				SegmentIndex: idx, KeyID: seg.KeyID, Status: report.StatusFailed,
				Reason: reason.SEGMENT_DUPLICATE,
				Detail: fmt.Sprintf("%d manifest entries share this index", count+1),
			})
			continue
		}
		if !inManifest {
			out = append(out, report.SegmentResult{
				SegmentIndex: idx, KeyID: "", Status: report.StatusFailed,
				Reason: reason.SEGMENT_MISSING, Detail: "no manifest entry for this index",
			})
			continue
		}
		if outOfOrder[idx] {
			out = append(out, report.SegmentResult{
				SegmentIndex: idx, KeyID: seg.KeyID, Status: report.StatusFailed,
				Reason: reason.SEGMENT_ORDER, Detail: "manifest position disagrees with segment index",
			})
			continue
		}
		out = append(out, validateOne(seg, delivered[idx], opts))
	}

	var extras []int
	for idx := range delivered {
		if idx < 0 || idx > maxIndex {
			extras = append(extras, idx)
		}
	}
	sort.Ints(extras)
	for _, idx := range extras {
		out = append(out, report.SegmentResult{
			SegmentIndex: idx, KeyID: "", Status: report.StatusFailed,
			Reason: reason.SEGMENT_UNEXPECTED,
			Detail: "ciphertext delivered for an index absent from the manifest",
		})
	}
	return out
}

func validateOne(seg asset.Segment, ciphertext []byte, opts ValidateOptions) report.SegmentResult {
	idx := seg.Index
	keyID := seg.KeyID
	if keyID == "" {
		return failResult(idx, "", reason.KEY_ID_MISMATCH, "manifest entry has no key id")
	}
	rawNonce, err := asset.DecodeNonce(seg.Nonce)
	if err != nil || len(rawNonce) != asset.NonceSize {
		return failResult(idx, keyID, reason.NONCE_INVALID, "manifest nonce is missing or malformed")
	}
	ct := ciphertext
	if len(ct) == 0 {
		return failResult(idx, keyID, reason.SEGMENT_MISSING, "no ciphertext delivered for segment %d", idx)
	}

	key, err := opts.Distributor.ResolveForDecrypt(keyID, opts.Now)
	if err != nil {
		code := reason.KEY_UNKNOWN
		switch {
		case errors.Is(err, issuer.ErrKeyExpired):
			// Expiry is terminal: never fall back to another version.
			code = reason.KEY_EXPIRED
		case errors.Is(err, issuer.ErrKeyNotFound):
			code = reason.KEY_UNKNOWN
		}
		return failResult(idx, keyID, code, "%v", err)
	}
	if opts.KeyMax > 0 && (key.Version < opts.KeyMin || key.Version > opts.KeyMax) {
		return failResult(idx, keyID, reason.KEY_OUT_OF_RANGE,
			"key version %d is outside the licensed range [%d,%d]", key.Version, opts.KeyMin, opts.KeyMax)
	}
	if key.Version != seg.KeyVersion && seg.KeyVersion != 0 {
		return failResult(idx, keyID, reason.KEY_ID_MISMATCH,
			"manifest key id %s implies version %d, got %d", keyID, seg.KeyVersion, key.Version)
	}

	aead, err := key.AEAD()
	if err != nil {
		return failResult(idx, keyID, reason.KEY_UNKNOWN, "%v", err)
	}
	plain, err := crypto.Open(aead, rawNonce, ct, opts.AssetID, idx)
	if err != nil {
		// Authentication runs before digest comparison so a flipped ciphertext
		// byte is reported as AUTH_FAILED rather than DIGEST_MISMATCH.
		return failResult(idx, keyID, reason.AUTH_FAILED, "%v (ciphertext may be corrupt or from another key)", err)
	}
	if got := asset.Digest(plain); got != seg.PlainDigest {
		return failResult(idx, keyID, reason.DIGEST_MISMATCH,
			"plaintext digest %s does not match manifest %s", short(got), short(seg.PlainDigest))
	}
	if got := asset.Digest(ct); got != seg.CipherDigest {
		return failResult(idx, keyID, reason.DIGEST_MISMATCH,
			"ciphertext digest %s does not match manifest %s", short(got), short(seg.CipherDigest))
	}
	return report.SegmentResult{
		SegmentIndex: idx, KeyID: keyID, Status: report.StatusOK, Reason: reason.NONE,
	}
}

// RotationsFromManifest derives the rotation log from the key identifiers
// recorded in the manifest.
func RotationsFromManifest(m *asset.Manifest) []report.Rotation {
	if m == nil {
		return nil
	}
	var rotations []report.Rotation
	prevID := ""
	prevVersion := 0
	count := 0
	for _, seg := range m.Segments {
		if prevID == "" {
			prevID, prevVersion = seg.KeyID, seg.KeyVersion
			continue
		}
		if seg.KeyVersion == prevVersion {
			continue
		}
		count++
		rotations = append(rotations, report.Rotation{
			Rotation:  count,
			FromKeyID: prevID,
			ToKeyID:   seg.KeyID,
			AtSegment: seg.Index,
		})
		prevID, prevVersion = seg.KeyID, seg.KeyVersion
	}
	return rotations
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxKey(m map[int]asset.Segment) int {
	best := -1
	for k := range m {
		if k > best {
			best = k
		}
	}
	return best
}
