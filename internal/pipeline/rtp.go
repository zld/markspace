package pipeline

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"example.com/mediavault/internal/clock"
	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/fingerprint"
	"example.com/mediavault/internal/manifest"
	"example.com/mediavault/internal/model"
	rtpc "example.com/mediavault/internal/rtp"
)

// ingestRTP parses serialized RTP packets, reconciles their timeline onto the
// shared sample base and archives the timeline products. A drift over the
// threshold, an ambiguous backward jump or an overflow isolates the asset.
func (r *Runner) ingestRTP(in Input) Report {
	rep := Report{Name: in.Name}
	if len(in.RTPPackets) == 0 {
		return r.reject(rep, model.StageReceived, fault.New(fault.CodeRTPSynth, fault.StageReceived, -1,
			"no RTP packets supplied for %s", in.Name))
	}
	rate := clock.NewRate(in.RTPSampleRate, 1)
	if !rate.Valid() {
		rate = clock.NewRate(48000, 1)
	}
	clockRate := in.RTPClockRate
	if clockRate == 0 {
		clockRate = 48000
	}
	frameBytes := in.RTPFrameBytes
	if frameBytes <= 0 {
		frameBytes = 2
	}

	var packets []rtp.Packet
	for i, raw := range in.RTPPackets {
		var p rtp.Packet
		if err := p.Unmarshal(raw); err != nil {
			return r.reject(rep, model.StageReceived, fault.Wrap(fault.CodeRTPSynth, fault.StageReceived, -1, err,
				"packet %d cannot be parsed", i))
		}
		packets = append(packets, p)
	}
	r.transition(&rep, in.Name, "", model.StageReceived)
	rep.Asset = model.Asset{
		Name:        in.Name,
		Transport:   model.TransportRTP,
		ChunkFrames: r.policy.ChunkFrames,
		Track: model.Track{
			ID:            in.Name,
			Transport:     model.TransportRTP,
			SampleRate:    rate,
			Channels:      1,
			BitsPerSample: 16,
			ClockRate:     clockRate,
		},
	}
	r.transition(&rep, in.Name, model.StageReceived, model.StageScanned)
	rep.Asset.Stage = model.StageScanned

	tl, err := rtpc.Reconcile(packets, rate, clockRate, frameBytes, rtpc.DefaultLimits())
	if err != nil {
		rep.Asset.Stage = model.StageNormalized
		return r.reject(rep, model.StageNormalized, err)
	}
	rep.Wraps = tl.Wraps
	rep.RTPPackets = len(tl.Packets)
	rep.DriftPPM = tl.DriftPPM
	rep.Asset.Track.TotalFrames = tl.TotalSamples
	rep.Asset.Track.StartTimestamp = packets[0].Timestamp
	r.transition(&rep, in.Name, model.StageScanned, model.StageFingerprinted)

	// The canonical timeline product is a compact binary stream of
	// (sequence, stamp, samples, position) records. It is archived as its own
	// content-addressed source, then chunked like PCM.
	timeline := encodeTimeline(tl)
	timelineSource := timelineDigest(timeline)
	if err := r.store.PutBytes(timelineSource, timeline); err != nil {
		return r.reject(rep, model.StageFingerprinted, err)
	}
	rep.Asset.SourceDigest = timelineSource
	scanID := resumeKey("rtp", timelineSource, r.policy.ChunkFrames)
	rec, err := r.replay()
	if err != nil {
		return r.reject(rep, model.StageFingerprinted, err)
	}
	prior := priorCheckpoints(rec, scanID)
	var refs []model.ChunkRef
	firstGap := -1
	for idx, frames := range blockLayout(int64(len(tl.Packets)), r.policy.ChunkFrames) {
		start := int64(idx) * r.policy.ChunkFrames
		end := start + frames
		pcm := encodePackets(tl.Packets[start:end])
		digest := fingerprint.ChunkDigest(idx, frames, pcm)
		ref := model.ChunkRef{Index: idx, Frames: frames, Length: int64(len(pcm)), Digest: digest}
		refs = append(refs, ref)
		if prev, ok := prior[idx]; ok && firstGap < 0 && prev.ContentHash == digest {
			if err := r.store.VerifyObject(digest, int64(len(pcm))); err != nil {
				if rewErr := r.store.RepairBytes(digest, pcm); rewErr != nil {
					return r.reject(rep, model.StageFingerprinted, fault.Wrap(fault.CodeCommitted, fault.StageFingerprinted, -1, err,
						"committed timeline block %d failed verification", idx))
				}
			}
			rep.Reused++
			rep.Committed++
			continue
		}
		if firstGap < 0 {
			firstGap = idx
			rep.ResumeFrom = idx
			fmt.Fprintf(r.out, "resume_from_block=%d asset=%s\n", idx, in.Name)
		}
		existed := r.store.Exists(digest)
		if err := r.store.PutBytes(digest, pcm); err != nil {
			return r.reject(rep, model.StageFingerprinted, err)
		}
		if !existed {
			rep.Recomputed++
			if err := r.store.VerifyObject(digest, int64(len(pcm))); err != nil {
				return r.reject(rep, model.StageFingerprinted, err)
			}
		} else {
			rep.Reused++
		}
		if err := r.recordCheckpoint(scanID, ref); err != nil {
			return r.reject(rep, model.StageFingerprinted, err)
		}
		rep.Committed++
		if r.stopAfterBlocks > 0 && rep.Committed >= r.stopAfterBlocks {
			rep.Interrupted = true
			rep.Asset.Stage = model.StageScanned
			return r.appendReport(rep)
		}
	}
	r.transition(&rep, in.Name, model.StageFingerprinted, model.StageNormalized)
	rep.Asset.Stage = model.StageNormalized
	if rep.ResumeFrom == 0 && rep.Committed == len(refs) {
		fmt.Fprintf(r.out, "resume_from_block=0 asset=%s\n", in.Name)
	}

	format := model.WAVFormat{SampleRate: rate, Channels: 1, BitsPerSample: 16}
	content, err := fingerprint.Aggregate(r.policy, format, refs)
	if err != nil {
		return r.reject(rep, model.StageNormalized, err)
	}
	rep.Asset.ContentDigest = content
	rep.Asset.PolicyVersion = r.policy.Version
	rep.Asset.Key = model.AssetKey(model.TransportRTP, content)
	rep.Asset.Chunks = refs
	rep.Asset.RecomputedBlocks = rep.Recomputed
	rep.Asset.ReusedBlocks = rep.Reused
	info := rtpInfo(tl)
	return r.archive(rep, refs, info)
}

// encodeTimeline frames the whole timeline for the resume identity.
func encodeTimeline(tl *rtpc.Timeline) []byte {
	out := make([]byte, 0, len(tl.Packets)*20)
	for _, p := range tl.Packets {
		out = appendPackets(out, p)
	}
	return out
}

func encodePackets(pkts []rtpc.Packet) []byte {
	out := make([]byte, 0, len(pkts)*20)
	for _, p := range pkts {
		out = appendPackets(out, p)
	}
	return out
}

func appendPackets(dst []byte, p rtpc.Packet) []byte {
	var b [20]byte
	binary.LittleEndian.PutUint16(b[0:2], p.Sequence)
	binary.LittleEndian.PutUint32(b[2:6], p.RawStamp)
	binary.BigEndian.PutUint64(b[6:14], p.Stamp)
	return append(dst, b[:14]...)
}

func timelineDigest(b []byte) string { return fingerprint.BlobDigest(b) }

// rtpInfo projects the reconciled timeline into the manifest.
func rtpInfo(tl *rtpc.Timeline) *manifest.RTPInfo {
	return &manifest.RTPInfo{
		Packets:      len(tl.Packets),
		Wraps:        tl.Wraps,
		DriftPPM:     tl.DriftPPM,
		Corrections:  tl.Corrections,
		TotalSamples: tl.TotalSamples,
	}
}
