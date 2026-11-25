package fixture

import (
	"os"
	"path/filepath"

	"rtpreplay/internal/rtplog"
	"rtpreplay/media"
)

// Damage kind strings recorded in the manifest and exercised by recovery.
const (
	DamageSeqGap        = "seq_gap"
	DamageWrap          = "timestamp_wrap"
	DamageDuplicate     = "duplicate_packet"
	DamageJump          = "forward_jump"
	DamageUnrecoverable = "oversized_gap"
	DamageMalformed     = "malformed_rtp"
	DamageTruncatedMP4  = "truncated_mp4"
	DamageBadBER        = "mxf_bad_ber"
)

// injectSeqGap removes n consecutive specs at index at.
func injectSeqGap(specs []Spec, at, n int) []Spec {
	return append(specs[:at], specs[at+n:]...)
}

// injectDuplicate repeats the spec at at (same sequence number and payload).
func injectDuplicate(specs []Spec, at int) []Spec {
	dup := specs[at]
	out := make([]Spec, 0, len(specs)+1)
	out = append(out, specs[:at+1]...)
	out = append(out, dup)
	out = append(out, specs[at+1:]...)
	return out
}

// injectJump rewrites the timestamp at at to a forward jump beyond the
// unwrapper's 10-second plausibility window.
func injectJump(specs []Spec, at int, rate uint32) []Spec {
	specs[at].TS = specs[at-1].TS + uint32(11*rate)
	return specs
}

// injectOversizedGap drops a run longer than the repair window (DefaultMaxGap).
func injectOversizedGap(specs []Spec, at int) []Spec {
	return injectSeqGap(specs, at, 32)
}

// damagedAudio applies the per-tier deterministic recoverable damages and
// returns the surviving specs plus the defect records.
func damagedAudio(seqStart uint16, tier int) ([]Spec, []Defect) {
	specs := AudioSpecs(seqStart, 0)
	var defects []Defect
	// Tier-independent anchors placed well inside healthy content spans.
	specs = injectSeqGap(specs, 30, 2)
	defects = append(defects, Defect{Kind: DamageSeqGap, Offset: int64(30), Detail: "two audio packets dropped"})
	specs = injectDuplicate(specs, 50)
	defects = append(defects, Defect{Kind: DamageDuplicate, Offset: int64(50), Detail: "repeated sequence number"})
	if tier == 0 {
		// Place the jump on the final packet: it yields an isolated anomaly
		// range without shifting the boundaries of the preceding content.
		last := len(specs) - 1
		specs = injectJump(specs, last, media.AudioClockRate)
		defects = append(defects, Defect{Kind: DamageJump, Offset: int64(last), Detail: "11-second timestamp jump on final packet"})
	}
	return specs, defects
}

// damagedVideo builds VFR specs whose timestamps cross the 32-bit boundary,
// plus a recoverable packet gap.
func damagedVideo() ([]Spec, []Defect) {
	specs := VideoSpecs(true) // wrap near the boundary
	defects := []Defect{{
		Kind: DamageWrap, Offset: 0, Detail: "timestamps straddle 2^32",
	}}
	specs = injectSeqGap(specs, 40, 2)
	defects = append(defects, Defect{Kind: DamageSeqGap, Offset: 40, Detail: "two video packets dropped"})
	return specs, defects
}

// GenerateDamaged writes recoverable damaged tracks and a pristine reference
// copy under <root>/reference, plus a truncated MP4 and MXF for container
// error-offset coverage.
func GenerateDamaged(root string, bitrates []int) error {
	if err := validateBitrates(bitrates); err != nil {
		return err
	}
	ref := filepath.Join(root, "reference")
	var allDefects []Defect
	for tier, kbps := range bitrates {
		// Healthy reference for the repaired-summary comparison.
		if err := WriteRTPLog(trackPath(ref, "audio", kbps), AudioSpecs(0, 0), AudioSSRC); err != nil {
			return err
		}
		if err := WriteRTPLog(trackPath(ref, "video", kbps), VideoSpecs(false), VideoSSRC); err != nil {
			return err
		}

		audioSpecs, aDefs := damagedAudio(0, tier)
		for i := range aDefs {
			aDefs[i].File = "audio/" + itoa(kbps) + ".rtplog"
		}
		allDefects = append(allDefects, aDefs...)
		if err := WriteRTPLog(trackPath(root, "audio", kbps), audioSpecs, AudioSSRC); err != nil {
			return err
		}

		videoSpecs, vDefs := damagedVideo()
		for i := range vDefs {
			vDefs[i].File = "video/" + itoa(kbps) + ".rtplog"
		}
		allDefects = append(allDefects, vDefs...)
		if err := WriteRTPLog(trackPath(root, "video", kbps), videoSpecs, VideoSSRC); err != nil {
			return err
		}
	}

	// Container-level damage for error-offset reporting.
	if err := os.MkdirAll(MetaDir(root), 0o755); err != nil {
		return err
	}
	mp4Bytes := sampleMP4()
	truncated := append([]byte(nil), mp4Bytes[:len(mp4Bytes)-12]...)
	if err := os.WriteFile(filepath.Join(MetaDir(root), "sample.mp4"), truncated, 0o644); err != nil {
		return err
	}
	allDefects = append(allDefects, Defect{
		File: "meta/sample.mp4", Kind: DamageTruncatedMP4,
		Offset: int64(len(truncated)), Detail: "last 12 bytes removed",
	})
	if err := os.WriteFile(filepath.Join(MetaDir(root), "sample.mxf"), brokenMXF(), 0o644); err != nil {
		return err
	}
	allDefects = append(allDefects, Defect{
		File: "meta/sample.mxf", Kind: DamageBadBER, Offset: 16, Detail: "BER length exceeds remaining bytes",
	})

	return WriteManifest(root, Manifest{
		Kind: "damaged", Bitrates: bitrates, Defects: allDefects, Reference: "reference",
	})
}

// brokenMXF is a valid partition key followed by an impossible BER length.
func brokenMXF() []byte {
	key := mxfKey("partition")
	out := append([]byte(nil), key[:]...)
	out = append(out, 0x80|0x04, 0x00, 0x10, 0x00, 0x00) // claims 1 MiB
	out = append(out, 1, 2, 3)
	return out
}

// GenerateUnrecoverable writes a track with an oversized hole beyond the
// repair window and a track with a malformed RTP record. Both conditions
// surface as exit code 2 even with recovery enabled.
func GenerateUnrecoverable(root string, bitrates []int) error {
	if err := validateBitrates(bitrates); err != nil {
		return err
	}
	kbps := bitrates[0]
	specs := injectOversizedGap(AudioSpecs(0, 0), 40)
	if err := WriteRTPLog(trackPath(root, "audio", kbps), specs, AudioSSRC); err != nil {
		return err
	}
	if err := WriteRTPLog(trackPath(root, "video", kbps), VideoSpecs(false), VideoSSRC); err != nil {
		return err
	}
	// A second, independently unparseable sidecar documents the malformed-
	// record condition without entering the main parse loop.
	if err := writeMalformedRTP(trackPath(root, "audio", kbps) + ".bad"); err != nil {
		return err
	}
	// Healthy containers keep metadata parsing orthogonal to the RTP hole.
	if err := os.MkdirAll(MetaDir(root), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(MetaDir(root), "sample.mp4"), HealthyMP4(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(MetaDir(root), "sample.mxf"), HealthyMXF(), 0o644); err != nil {
		return err
	}
	return WriteManifest(root, Manifest{
		Kind: "unrecoverable", Bitrates: bitrates,
		Defects: []Defect{
			{File: "audio/" + itoa(kbps) + ".rtplog", Kind: DamageUnrecoverable, Offset: 40, Detail: "32-packet hole beyond repair window"},
			{File: "audio/" + itoa(kbps) + ".rtplog.bad", Kind: DamageMalformed, Offset: 0, Detail: "2-byte RTP record"},
		},
	})
}

func writeMalformedRTP(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := rtplog.WriteHeader(f); err != nil {
		return err
	}
	return rtplog.WriteRecord(f, unixBaseTime(), []byte{0x00, 0x01})
}
