// Package metrics computes post-remux quality signals: bitrate deviation,
// sample accounting, payload hash verification and cross-track timeline
// alignment. It reads only the input events and the output manifest, so
// it never touches decoded media.
package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"

	"mediatool/internal/remux"
	"mediatool/internal/timeline"
)

// SlotTarget pairs an output slot with its configured target bitrate.
type SlotTarget struct {
	Slot         string
	TargetBitBPS int
}

// Report is the deterministic metrics result for one run.
type Report struct {
	TargetBitrateBPS    int        `json:"target_bitrate_bps"`
	ActualBitrateBPS    int        `json:"actual_bitrate_bps"`
	BitrateDeviationPct float64    `json:"bitrate_deviation_pct"`
	SampleHashMatch     bool       `json:"sample_hash_match"`
	QualitySamples      int        `json:"quality_samples"`
	ManifestAlignmentMS float64    `json:"manifest_alignment_ms"`
	SlotReports         []SlotReport `json:"slot_reports"`
}

// SlotReport holds per-track bitrate and sample figures.
type SlotReport struct {
	Slot             string
	MediaType        string
	TargetBitrateBPS int
	ActualBitrateBPS int
	DeviationPct     float64
	PayloadBytes     int
	DurationMS       float64
	Samples          int
	HashesVerified   int
	HashesFailed     int
}

// Compute evaluates the manifest against the input event hashes and the
// per-slot target bitrates. Input events provide the pre-remux payload
// hashes; the manifest carries the post-remux hashes for comparison.
func Compute(manifest *remux.Manifest, events []*timeline.SampleEvent,
	targets []SlotTarget) Report {
	targetBySlot := make(map[string]int, len(targets))
	var totalTarget int
	for _, t := range targets {
		targetBySlot[t.Slot] = t.TargetBitBPS
		totalTarget += t.TargetBitBPS
	}

	trackSlot := make(map[uint32]string)
	for _, s := range manifest.Slots {
		trackSlot[s.TrackID] = s.Slot
	}
	// Input hash index keyed by slot and original sequence number. Repeated
	// events never produced an output chunk, so they are excluded.
	inputHashes := make(map[string][32]byte)
	for _, ev := range events {
		slot, ok := trackSlot[ev.TrackID]
		if !ok || ev.Repeated {
			continue
		}
		inputHashes[hashKey(slot, ev.Seq)] = ev.PayloadHash
	}

	rep := Report{SampleHashMatch: true}
	var totalActual, qualitySamples, totalFailed int
	for _, slot := range manifest.Slots {
		sr := measureSlot(slot, targetBySlot[slot.Slot], inputHashes)
		rep.SlotReports = append(rep.SlotReports, sr)
		totalActual += sr.ActualBitrateBPS
		qualitySamples += sr.Samples
		totalFailed += sr.HashesFailed
	}
	if len(rep.SlotReports) == 0 || totalFailed > 0 {
		rep.SampleHashMatch = false
	}
	rep.TargetBitrateBPS = totalTarget
	rep.ActualBitrateBPS = totalActual
	if totalTarget > 0 {
		rep.BitrateDeviationPct = round2(
			float64(totalActual-totalTarget) / float64(totalTarget) * 100)
	}
	rep.QualitySamples = qualitySamples
	rep.ManifestAlignmentMS = measureAlignment(manifest)
	return rep
}

func measureSlot(slot remux.SlotManifest, targetBPS int,
	inputHashes map[string][32]byte) SlotReport {
	sr := SlotReport{Slot: slot.Slot, MediaType: slot.MediaType,
		TargetBitrateBPS: targetBPS}
	var payloadBytes, durationUnits uint64
	for _, f := range slot.Fragments {
		payloadBytes += uint64(f.PayloadBytes)
		durationUnits += f.Duration
		sr.Samples += f.SampleCount
		want, ok := inputHashes[hashKey(slot.Slot, f.OriginSeq)]
		if !ok {
			sr.HashesFailed++
			continue
		}
		if hex.EncodeToString(want[:]) == f.PayloadHashHex {
			sr.HashesVerified++
		} else {
			sr.HashesFailed++
		}
	}
	sr.PayloadBytes = int(payloadBytes)
	if slot.Timescale > 0 {
		sr.DurationMS = float64(durationUnits) * 1000 / float64(slot.Timescale)
	}
	if sr.DurationMS > 0 {
		sr.ActualBitrateBPS = int(math.Round(
			float64(payloadBytes) * 8 * 1000 / sr.DurationMS))
	}
	if targetBPS > 0 {
		sr.DeviationPct = round2(
			float64(sr.ActualBitrateBPS-targetBPS) / float64(targetBPS) * 100)
	}
	return sr
}

// measureAlignment compares video and audio fragment start times on a
// common millisecond axis and returns the largest absolute start-time
// difference across paired fragments.
func measureAlignment(manifest *remux.Manifest) float64 {
	var video, audio *remux.SlotManifest
	for i := range manifest.Slots {
		switch manifest.Slots[i].MediaType {
		case "video":
			video = &manifest.Slots[i]
		case "audio":
			audio = &manifest.Slots[i]
		}
	}
	if video == nil || audio == nil ||
		len(video.Fragments) == 0 || len(audio.Fragments) == 0 {
		return 0
	}
	pairs := len(video.Fragments)
	if len(audio.Fragments) < pairs {
		pairs = len(audio.Fragments)
	}
	worst := 0.0
	for i := 0; i < pairs; i++ {
		vf := video.Fragments[i]
		af := audio.Fragments[i]
		vMS := float64(vf.CorrectedDTS) * 1000 / float64(video.Timescale)
		aMS := float64(af.CorrectedDTS) * 1000 / float64(audio.Timescale)
		if d := math.Abs(vMS - aMS); d > worst {
			worst = d
		}
	}
	return round2(worst)
}

func hashKey(slot string, originSeq int) string {
	return slot + "|" + strconv.Itoa(originSeq)
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// HashBytes exposes the payload hashing convention used across the
// pipeline so callers hash with identical settings.
func HashBytes(b []byte) [32]byte { return sha256.Sum256(b) }
