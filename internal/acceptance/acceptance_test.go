// Package acceptance is a single entry point that exercises the five
// capability categories the project must demonstrate numerically: unknown-
// length EBML parsing, stable track mapping, truncation localization, index
// checksum validation and RTP sequence anomalies. It prints a pass count per
// category and fails loudly if any regression removes a required guarantee.
package acceptance

import (
	"bytes"
	"fmt"
	"testing"

	"mediapack/internal/checkpoint"
	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/fixture"
	"mediapack/internal/rtp"
	"mediapack/internal/track"
)

// category counts one acceptance capability.
type category struct {
	name  string
	pass  int
	total int
}

// TestAcceptanceReport runs every required category and reports pass counts.
func TestAcceptanceReport(t *testing.T) {
	var cats []category

	cats = append(cats, category{
		name: "unknown_length_ebml_parsing",
		pass: countTrue(
			unknownLengthParses(t),
			unknownLengthMultiCluster(t),
		), total: 2,
	})
	cats = append(cats, category{
		name: "track_mapping_stability",
		pass: countTrue(
			mappingStableAcrossRuns(t),
			mappingIndicesSequential(t),
		), total: 2,
	})
	cats = append(cats, category{
		name: "truncation_localization",
		pass: countTrue(
			truncationOffsetWithinOneByte(t),
			truncationIsRecoverable(t),
		), total: 2,
	})
	cats = append(cats, category{
		name: "index_checksum_validation",
		pass: countTrue(
			corruptIndexRejected(t),
			inputChangeRejected(t),
		), total: 2,
	})
	cats = append(cats, category{
		name: "rtp_sequence_anomalies",
		pass: countTrue(
			rtpJumpDetected(t),
			rtpDuplicateDetected(t),
		), total: 2,
	})

	totalPass, totalCases := 0, 0
	t.Log("acceptance summary:")
	for _, c := range cats {
		t.Logf("  %s: %d/%d passed", c.name, c.pass, c.total)
		totalPass += c.pass
		totalCases += c.total
		if c.pass != c.total {
			t.Errorf("category %s failed %d case(s)", c.name, c.total-c.pass)
		}
	}
	t.Logf("TOTAL: %d/%d acceptance cases passed", totalPass, totalCases)
	fmt.Printf("acceptance total: %d/%d passed\n", totalPass, totalCases)
}

func countTrue(vals ...bool) int {
	n := 0
	for _, v := range vals {
		if v {
			n++
		}
	}
	return n
}

func sampleSpec() fixture.Spec {
	return fixture.Spec{
		Seed: 7, Codecs: []string{"V_VP9", "A_OPUS", "S_TEXT/WEBVTT"},
		Clusters: 3, FramesPerTrack: 2, PayloadMin: 24, PayloadMax: 48,
	}
}

// --- category 1: unknown-length EBML -----------------------------------

func unknownLengthParses(t *testing.T) bool {
	t.Helper()
	spec := sampleSpec()
	spec.UnknownLength = true
	raw, _ := fixture.Build(spec, fixture.CorruptNone)
	d, err := demux.Open(bytes.NewReader(raw))
	if err != nil {
		t.Errorf("open unknown-length: %v", err)
		return false
	}
	n := 0
	if err := d.Stream(func(p *demux.Packet) error { n++; return nil }); err != nil {
		t.Errorf("stream unknown-length: %v", err)
		return false
	}
	return n > 0
}

func unknownLengthMultiCluster(t *testing.T) bool {
	t.Helper()
	spec := sampleSpec()
	spec.UnknownLength = true
	spec.Clusters = 4
	raw, _ := fixture.Build(spec, fixture.CorruptNone)
	d, err := demux.Open(bytes.NewReader(raw))
	if err != nil {
		t.Errorf("open multi unknown-length: %v", err)
		return false
	}
	// All clusters must be discovered despite unknown cluster sizes.
	n := 0
	if err := d.Stream(func(p *demux.Packet) error { n++; return nil }); err != nil {
		t.Errorf("stream multi unknown-length: %v", err)
		return false
	}
	return n == spec.Clusters*len(spec.Codecs)*spec.FramesPerTrack
}

// --- category 2: track mapping stability --------------------------------

func mappingStableAcrossRuns(t *testing.T) bool {
	t.Helper()
	build := func() *track.Mapping {
		raw, _ := fixture.Build(sampleSpec(), fixture.CorruptNone)
		d, err := demux.Open(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return d.Mapping()
	}
	return build().Equal(build())
}

func mappingIndicesSequential(t *testing.T) bool {
	t.Helper()
	raw, _ := fixture.Build(sampleSpec(), fixture.CorruptNone)
	d, err := demux.Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for i, tr := range d.Mapping().Tracks {
		if tr.OutputIndex != uint32(i+1) {
			t.Errorf("output index %d not sequential at %d", tr.OutputIndex, i)
			return false
		}
	}
	return true
}

// --- category 3: truncation localization --------------------------------

func truncationOffsetWithinOneByte(t *testing.T) bool {
	t.Helper()
	raw, detail := fixture.Build(sampleSpec(), fixture.CorruptTruncate)
	d, err := demux.Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	err = d.Stream(func(p *demux.Packet) error { return nil })
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindTruncated {
		t.Errorf("expected truncation, got %v", err)
		return false
	}
	delta := fe.Offset - detail.CorruptOffset
	return delta >= -1 && delta <= 1
}

func truncationIsRecoverable(t *testing.T) bool {
	t.Helper()
	raw, _ := fixture.Build(sampleSpec(), fixture.CorruptTruncate)
	d, _ := demux.Open(bytes.NewReader(raw))
	err := d.Stream(func(p *demux.Packet) error { return nil })
	fe := fault.Of(err)
	return fe != nil && fe.Recoverable
}

// --- category 4: index checksum ------------------------------------------

func corruptIndexRejected(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	input := dir + "/in.webm"
	data := []byte("input-bytes-for-identity")
	if err := writeFile(input, data); err != nil {
		t.Fatal(err)
	}
	sum, _, _ := checkpoint.FileSHA256(input)
	store, err := checkpoint.OpenStore(dir+"/idx", 2, input, sum)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(int64(len(data)), 1, [][32]byte{{1}}); err != nil {
		t.Fatal(err)
	}
	// Corrupt the index file.
	path := store.Path()
	raw, _ := readFile(path)
	raw[len(raw)-30] ^= 0x01
	if err := writeFile(path, raw); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load()
	fe := fault.Of(err)
	return fe != nil && fe.Kind == fault.KindCheckpointCorrupt && fe.Recoverable && fe.Offset >= 0
}

func inputChangeRejected(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	input := dir + "/in.webm"
	if err := writeFile(input, []byte("original-bytes-here")); err != nil {
		t.Fatal(err)
	}
	sum, _, _ := checkpoint.FileSHA256(input)
	store, _ := checkpoint.OpenStore(dir+"/idx", 2, input, sum)
	if _, err := store.Commit(int64(len("original-bytes-here")), 1, [][32]byte{{1}}); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(input, []byte("totally-different-bytes!")); err != nil {
		t.Fatal(err)
	}
	sum2, _, _ := checkpoint.FileSHA256(input)
	store2, _ := checkpoint.OpenStore(dir+"/idx", 2, input, sum2)
	_, err := store2.Load()
	fe := fault.Of(err)
	return fe != nil && fe.Kind == fault.KindInputChanged && fe.Recoverable
}

// --- category 5: RTP sequence --------------------------------------------

func rtpJumpDetected(t *testing.T) bool {
	t.Helper()
	v := rtp.NewVerifier()
	if _, err := v.Check(rtpDatagram(10, []byte("a")), 0, 1); err != nil {
		return false
	}
	_, err := v.Check(rtpDatagram(14, []byte("b")), 16, 1)
	fe := fault.Of(err)
	return fe != nil && fe.Kind == fault.KindRTPSequence && fe.Offset == 16
}

func rtpDuplicateDetected(t *testing.T) bool {
	t.Helper()
	v := rtp.NewVerifier()
	if _, err := v.Check(rtpDatagram(5, []byte("a")), 0, 1); err != nil {
		return false
	}
	if _, err := v.Check(rtpDatagram(6, []byte("b")), 8, 1); err != nil {
		return false
	}
	_, err := v.Check(rtpDatagram(5, []byte("c")), 16, 1)
	fe := fault.Of(err)
	return fe != nil && fe.Kind == fault.KindRTPSequence && fe.Offset == 16
}
