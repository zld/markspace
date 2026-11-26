package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/fingerprint"
	"example.com/mediavault/internal/model"
	"example.com/mediavault/internal/store"
	"example.com/mediavault/internal/wav"
)

// Input is one source to ingest. Kind selects the subsystem: "wav" or "rtp".
type Input struct {
	Name string
	Path string
	Kind string
	// RTP inputs need their payload frame size and declared sample rate so the
	// timeline can map timestamps onto the shared base.
	RTPFrameBytes int
	RTPSampleRate int64
	RTPClockRate  uint32
	RTPPackets    [][]byte
}

type block struct {
	index  int
	frames int64
	pcm    []byte
	digest string
}

// resumeKey is the checkpoint identity. It is anchored to the full source
// digest and the block policy, never to the path or modification time: two
// byte-identical copies resume each other's checkpoints, while any source edit
// (including a container-only one) starts a fresh checkpoint sequence.
func resumeKey(kind, sourceDigest string, chunkFrames int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "mediavault\x00resume\x00%s\x00%s\x00%d\x00%s",
		kind, sourceDigest, chunkFrames, fingerprint.PolicyVersion)
	return kind + ":scan:" + hex.EncodeToString(h.Sum(nil))
}

// priorCheckpoints returns committed checkpoints for a resume key keyed by block.
func priorCheckpoints(rec *store.Recorder, key string) map[int]store.Record {
	out := make(map[int]store.Record)
	for _, r := range rec.Records() {
		if r.Kind == "checkpoint" && r.AssetKey == key {
			out[r.Block] = r
		}
	}
	return out
}

// blockLayout derives the number of blocks a frame count occupies.
func blockLayout(frames, chunkFrames int64) []int64 {
	if chunkFrames <= 0 {
		return nil
	}
	var out []int64
	for f := int64(0); f < frames; f += chunkFrames {
		n := chunkFrames
		if frames-f < n {
			n = frames - f
		}
		out = append(out, n)
	}
	return out
}

// ingestWAV runs RECEIVED -> SCANNED -> FINGERPRINTED -> NORMALIZED -> ARCHIVED
// for one WAV file, committing each block as soon as it is durable.
func (r *Runner) ingestWAV(in Input) Report {
	rep := Report{Name: in.Name}
	f, info, err := wav.ScanFile(in.Path)
	if err != nil {
		return r.reject(rep, model.StageReceived, err)
	}
	defer f.Close()
	// The source bytes are archived under their own digest, read-only, before
	// any derived work is trusted. Identity and recovery both key off content.
	if err := r.store.PutFile(info.SourceDigest, in.Path, info.FileSize); err != nil {
		return r.reject(rep, model.StageReceived, err)
	}
	rep.Asset = model.Asset{
		Name:            in.Name,
		Path:            in.Path,
		Transport:       model.TransportWAV,
		SourceDigest:    info.SourceDigest,
		ContainerDigest: info.ContainerDigest,
		ChunkFrames:     r.policy.ChunkFrames,
		Track: model.Track{
			ID:            in.Name,
			Transport:     model.TransportWAV,
			Format:        info.Format,
			SampleRate:    info.Format.SampleRate,
			Channels:      info.Format.Channels,
			BitsPerSample: info.Format.BitsPerSample,
			TotalFrames:   info.Frames,
		},
	}
	r.transition(&rep, in.Name, "", model.StageReceived)
	rep.Asset.Stage = model.StageReceived
	r.transition(&rep, in.Name, model.StageReceived, model.StageScanned)
	rep.Asset.Stage = model.StageScanned
	scanID := resumeKey("wav", info.SourceDigest, r.policy.ChunkFrames)

	rec, err := r.replay()
	if err != nil {
		return r.reject(rep, model.StageScanned, err)
	}
	prior := priorCheckpoints(rec, scanID)

	rd, err := wav.NewReader(f, info)
	if err != nil {
		return r.reject(rep, model.StageScanned, err)
	}
	var (
		refs     []model.ChunkRef
		firstGap = -1
	)
	for idx, frames := range blockLayout(info.Frames, r.policy.ChunkFrames) {
		pcm, err := rd.ReadFrames(int64(idx)*r.policy.ChunkFrames, frames)
		if err != nil {
			return r.reject(rep, model.StageScanned, err)
		}
		digest := fingerprint.ChunkDigest(idx, frames, pcm)
		ref := model.ChunkRef{Index: idx, Frames: frames, Length: int64(len(pcm)), Digest: digest}
		refs = append(refs, ref)

		if prev, ok := prior[idx]; ok && firstGap < 0 && prev.ContentHash == digest {
			// Recovery must prove the checkpoint still describes these bytes,
			// including the stored object itself, before skipping the block.
			if err := r.store.VerifyObject(digest, int64(len(pcm))); err != nil {
				return r.reject(rep, model.StageScanned, fault.Wrap(fault.CodeCommitted, fault.StageScanned,
					info.DataOffset+int64(idx)*int64(info.Format.FrameBytes()), err,
					"committed block %d failed verification", idx))
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
			fmt.Fprintf(r.out, "interrupted %s after %d committed blocks (stop_after_blocks=%d)\n", in.Name, rep.Committed, r.stopAfterBlocks)
			return r.appendReport(rep)
		}
	}
	if rep.ResumeFrom == 0 && rep.Committed == len(refs) {
		fmt.Fprintf(r.out, "resume_from_block=0 asset=%s\n", in.Name)
	}
	rep.Asset.CommittedBlocks = rep.Committed
	r.transition(&rep, in.Name, model.StageScanned, model.StageFingerprinted)
	rep.Asset.Stage = model.StageFingerprinted

	content, err := fingerprint.Aggregate(r.policy, info.Format, refs)
	if err != nil {
		return r.reject(rep, model.StageFingerprinted, err)
	}
	rep.Asset.ContentDigest = content
	rep.Asset.PolicyVersion = r.policy.Version
	rep.Asset.Key = model.AssetKey(model.TransportWAV, content)
	rep.Asset.Chunks = refs
	rep.Asset.RecomputedBlocks = rep.Recomputed
	rep.Asset.ReusedBlocks = rep.Reused

	r.transition(&rep, in.Name, model.StageFingerprinted, model.StageNormalized)
	rep.Asset.Stage = model.StageNormalized
	return r.archive(rep, refs, nil)
}

// openSource is a seam used by tests to substitute a reader.
var openSource = io.Discard
