package fixture

import (
	"errors"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/ingest"
	"mediapipeline/internal/merge"
)

// truncateUnit is the unit whose framing byte the truncation injection cuts
// into.  Cutting one byte into its declared length yields a NAL_TRUNCATED /
// OBU_TRUNCATED fault located at the unit start offset.
const truncateUnit = FrameCount - 2

// applyTruncate cuts the stream one byte into the target unit's framing so the
// scanner reaches a partial length prefix / OBU header at the target offset and
// reports NAL_TRUNCATED / OBU_TRUNCATED there.  The injected framing offset and
// the detected fault offset are therefore the same byte.
func applyTruncate(s *Sample) {
	off := streamOffset(s.Codec, s.Payloads, truncateUnit)
	s.TruncateOffset = off
	cut := off + 1 // keep exactly one framing byte: a legal start, an incomplete unit
	if cut > int64(len(s.Stream)) {
		cut = int64(len(s.Stream))
	}
	s.Stream = s.Stream[:cut]
}

// streamOffset returns the framing byte offset of unit idx.
func streamOffset(codec ingest.Codec, payloads [][]byte, idx int) int64 {
	var off int64
	for i := 0; i < idx; i++ {
		off += unitFraming(codec, payloads[i])
	}
	return off
}

// buildVersions maps key units onto three asset keys across three versions.
// All versions agree in clean samples; truncation leaves the last asset
// pointing at a unit the rescanned (cut) stream no longer contains.
func buildVersions(s *Sample) []merge.Version {
	keyUnits := keyUnitIndices()
	keys := []string{"asset-key-0", "asset-key-1", "asset-key-2"}
	makeEntries := func(version string) []merge.Entry {
		var entries []merge.Entry
		for i, u := range keyUnits {
			if i >= len(keys) {
				break
			}
			body := s.Payloads[u]
			entries = append(entries, merge.Entry{
				Asset: merge.Asset{
					Key: keys[i], Version: version, UnitIndex: u,
					Offset:   streamOffset(s.Codec, s.Payloads, u),
					Size:     int64(len(body)),
					TimeFrom: clock.MustRational(int64(u*ticksPerUnit), indexTimeBaseDen),
					TimeTo:   clock.MustRational(int64((u+1)*ticksPerUnit), indexTimeBaseDen),
				},
				Payload: append([]byte(nil), body...),
			})
		}
		return entries
	}
	return []merge.Version{
		{Name: "v1", Entries: makeEntries("v1")},
		{Name: "v2", Entries: makeEntries("v2")},
		{Name: "v3", Entries: makeEntries("v3")},
	}
}

func keyUnitIndices() []int {
	var out []int
	for i := 0; i < FrameCount; i += KeyStride {
		out = append(out, i)
	}
	return out
}

// feedResult carries the transport-injected feed and the bookkeeping the demo
// reports alongside it.
type feedResult struct {
	packets   [][]byte
	lost      []uint16
	dups      int
	reordered bool
}

// buildFeed starts from the canonical in-order packets and applies duplicate,
// reorder and burst-loss injections, recording lost raw sequence numbers.
func buildFeed(payloads [][]byte, enabled map[string]bool) feedResult {
	base := packetise(payloads)

	// burst-loss: drop a contiguous run in the middle of the feed.
	var lost []uint16
	keep := make([][]byte, 0, len(base))
	lossStart, lossEnd := 5, 8 // drop sequences 5,6,7
	for seq, raw := range base {
		if enabled[InjectBurstLoss] && seq >= lossStart && seq < lossEnd {
			lost = append(lost, uint16(seq))
			continue
		}
		keep = append(keep, raw)
	}

	// reorder: swap two adjacent surviving packets (sequences 2 and 3).
	reordered := false
	if enabled[InjectReorder] && len(keep) > 4 {
		keep[2], keep[3] = keep[3], keep[2]
		reordered = true
	}

	// duplicate: repeat the first surviving packet verbatim, then continue the
	// feed from the second surviving packet so the first sequence is emitted
	// exactly twice and counted once as a duplicate.
	dups := 0
	var feed [][]byte
	if enabled[InjectDuplicate] && len(keep) > 0 {
		feed = append(feed, keep[0], keep[0])
		feed = append(feed, keep[1:]...)
		dups = 1
	} else {
		feed = append(feed, keep...)
	}
	return feedResult{packets: feed, lost: lost, dups: dups, reordered: reordered}
}

func validateCodec(c ingest.Codec) error {
	switch c {
	case ingest.CodecH264, ingest.CodecH265, ingest.CodecAV1:
		return nil
	default:
		return errors.New("fixture: unknown codec " + string(c))
	}
}

func knownInjection(name string) bool {
	switch name {
	case InjectTruncate, InjectIndexOOB, InjectDuplicate,
		InjectReorder, InjectBurstLoss, InjectBandwidthDrop:
		return true
	default:
		return false
	}
}

func errUnknownInjection(name string) error {
	return errors.New("fixture: unknown injection " + name)
}
