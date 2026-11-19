package pipeline

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"mediakey/internal/asset"
	"mediakey/internal/license"
	"mediakey/internal/reason"
	"mediakey/internal/replay"
	"mediakey/internal/report"
)

// Validate authenticates and decrypts every segment it can.
//
// It first admits the license (and consumes its serial when a replay cache is
// configured), then walks every expected segment index independently. A
// failure in one segment never stops the others. Failure classification is
// ordered so the reported reason is the most actionable: license, then
// manifest structure, then nonce and key resolution, then GCM authentication,
// then digest agreement.
func Validate(m *asset.Manifest, lic *license.License, delivered map[int][]byte, opts ValidateOptions) *Outcome {
	out := &Outcome{
		License:  admitLicense(opts.Secret, lic, opts.Now, opts.AssetID, opts.Replay),
		Segments: []report.SegmentResult{},
	}
	if m == nil {
		out.Manifest = report.ManifestCheck{
			Checked: true, Status: report.StatusFailed, Reason: reason.SEGMENT_COUNT_MISMATCH,
			Detail: "no manifest supplied",
		}
		return out
	}
	out.Manifest = checkManifestStructure(m, delivered)

	if out.License.Status != report.StatusOK {
		out.Segments = licenseBlockedSegments(m, delivered, out.License.Reason)
		return out
	}
	out.Segments = segmentResults(m, delivered, opts)
	return out
}

// admitLicense validates the license and registers its serial. Replay
// registration happens only after signature, scope and expiry checks pass, so
// an expired license is reported as LICENSE_EXPIRED rather than consuming a
// fresh serial.
func admitLicense(secret []byte, lic *license.License, now time.Time, assetID string, cache *replay.Cache) report.LicenseResult {
	res := report.LicenseResult{Status: report.StatusFailed, Reason: reason.LICENSE_INVALID}
	if lic != nil {
		res.Serial = lic.Serial
		res.KeyMin = lic.KeyMin
		res.KeyMax = lic.KeyMax
		res.IssuedAt = lic.IssuedAt
		res.ExpiresAt = lic.ExpiresAt
	}
	if err := lic.Check(secret, now, assetID); err != nil {
		res.Reason = license.ReasonCode(err)
		res.Detail = err.Error()
		return res
	}
	if cache != nil {
		if err := cache.Register(lic.Serial, assetID, now.UnixMilli()); err != nil {
			switch {
			case errors.Is(err, replay.ErrReplay):
				res.Reason = reason.LICENSE_REPLAY
			default:
				res.Reason = reason.LICENSE_MISSING_FIELD
			}
			res.Detail = err.Error()
			return res
		}
	}
	res.Status = report.StatusOK
	res.Reason = reason.NONE
	return res
}

// checkManifestStructure compares the declared shape of the manifest with the
// segments actually present, without authenticating any content.
func checkManifestStructure(m *asset.Manifest, delivered map[int][]byte) report.ManifestCheck {
	mc := report.ManifestCheck{
		Checked:  true,
		Declared: m.SegmentCount,
		Present:  len(m.Segments),
		Status:   report.StatusOK,
		Reason:   reason.NONE,
	}
	if m.SegmentCount != 0 && m.SegmentCount != len(m.Segments) {
		mc.Status = report.StatusFailed
		mc.Reason = reason.SEGMENT_COUNT_MISMATCH
		mc.Detail = fmt.Sprintf("declared %d segments, manifest lists %d", m.SegmentCount, len(m.Segments))
		return mc
	}
	prev := -1
	for _, seg := range m.Segments {
		if seg.Index <= prev {
			mc.Status = report.StatusFailed
			mc.Reason = reason.SEGMENT_ORDER
			mc.Detail = fmt.Sprintf("segment index %d follows %d", seg.Index, prev)
			return mc
		}
		prev = seg.Index
	}
	if extra := extraDelivered(m, delivered); extra >= 0 {
		mc.Status = report.StatusFailed
		mc.Reason = reason.SEGMENT_COUNT_MISMATCH
		mc.Detail = fmt.Sprintf("delivered segment %d is not in the manifest", extra)
	}
	return mc
}

func extraDelivered(m *asset.Manifest, delivered map[int][]byte) int {
	known := make(map[int]struct{}, len(m.Segments))
	for _, seg := range m.Segments {
		known[seg.Index] = struct{}{}
	}
	extras := make([]int, 0)
	for idx := range delivered {
		if _, ok := known[idx]; !ok {
			extras = append(extras, idx)
		}
	}
	if len(extras) == 0 {
		return -1
	}
	sort.Ints(extras)
	return extras[0]
}

// licenseBlockedSegments reports one result per known segment, all carrying
// the license failure reason, so the per-segment listing remains complete
// even though no key was used.
func licenseBlockedSegments(m *asset.Manifest, delivered map[int][]byte, code reason.Code) []report.SegmentResult {
	byIndex := make(map[int]asset.Segment, len(m.Segments))
	for _, seg := range m.Segments {
		byIndex[seg.Index] = seg
	}
	for idx := range delivered {
		if _, ok := byIndex[idx]; !ok {
			byIndex[idx] = asset.Segment{Index: idx}
		}
	}
	out := make([]report.SegmentResult, 0, len(byIndex))
	for idx, seg := range byIndex {
		out = append(out, report.SegmentResult{
			SegmentIndex: idx,
			KeyID:        seg.KeyID,
			Status:       report.StatusFailed,
			Reason:       code,
			Detail:       "license not admitted; segment not decrypted",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SegmentIndex < out[j].SegmentIndex })
	return out
}
