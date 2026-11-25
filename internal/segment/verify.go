package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// VerificationReport records the four required manifest checks.
type VerificationReport struct {
	OrderingOK          bool
	TimeContinuityOK    bool
	RepresentationSetOK bool
	FileDigestOK        bool
	Errors              []string
}

// OK is true only when every structural and digest check passes.
func (r VerificationReport) OK() bool {
	return r.OrderingOK && r.TimeContinuityOK && r.RepresentationSetOK && r.FileDigestOK
}

// Verify checks ordering, time continuity, the representation set and the
// input file digest. It never repairs a problem; each failure is reported.
func Verify(m *Manifest, input []byte, expectedTracks []uint32) VerificationReport {
	r := VerificationReport{
		OrderingOK:          true,
		TimeContinuityOK:    true,
		RepresentationSetOK: true,
		FileDigestOK:        true,
	}

	for i := 1; i < len(m.Segments); i++ {
		prev, cur := m.Segments[i-1], m.Segments[i]
		if cur.Index <= prev.Index || cur.Start < prev.Start {
			r.OrderingOK = false
			r.Errors = append(r.Errors,
				fmt.Sprintf("segment %d breaks ordering after %d", cur.Index, prev.Index))
		}
		if cur.Start != prev.End {
			r.TimeContinuityOK = false
			r.Errors = append(r.Errors,
				fmt.Sprintf("gap/overlap between segments %d and %d: %d -> %d",
					prev.Index, cur.Index, prev.End, cur.Start))
		}
	}

	want := map[uint32]bool{}
	for _, id := range expectedTracks {
		want[id] = true
	}
	got := map[uint32]bool{}
	for _, id := range m.TrackIDs {
		got[id] = true
	}
	if len(got) != len(want) {
		r.RepresentationSetOK = false
		r.Errors = append(r.Errors, "representation set size differs")
	}
	for id := range want {
		if !got[id] {
			r.RepresentationSetOK = false
			r.Errors = append(r.Errors, fmt.Sprintf("representation %d absent from manifest", id))
		}
	}
	for _, seg := range m.Segments {
		for id := range want {
			if _, ok := seg.Members[id]; !ok {
				r.RepresentationSetOK = false
				r.Errors = append(r.Errors,
					fmt.Sprintf("segment %d missing representation %d", seg.Index, id))
			}
		}
	}

	sum := sha256.Sum256(input)
	actual := hex.EncodeToString(sum[:])
	if actual != m.InputDigest {
		r.FileDigestOK = false
		r.Errors = append(r.Errors, "input digest does not match manifest")
	}

	// Recompute the segment digest to prove the manifest is internally stable.
	recomputed, err := m.computeDigest()
	if err != nil || recomputed != m.SegmentDigest {
		r.FileDigestOK = false
		r.Errors = append(r.Errors, "segment digest not reproducible")
	}
	if m.MaxDelta > int64(MaxBoundaryDelta) {
		r.Errors = append(r.Errors, fmt.Sprintf("boundary delta %d exceeds one tick", m.MaxDelta))
	}
	return r
}

// DigestInput returns the hex SHA-256 of raw input, shared with the pipeline.
func DigestInput(input []byte) string {
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}
