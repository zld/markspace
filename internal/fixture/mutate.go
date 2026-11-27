package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"

	"continuity/internal/clock"
	"continuity/internal/model"
)

func rtpTimestampOf(captureNanos int64) (uint32, error) {
	return clock.RTPTimestamp(captureNanos, clock.MediaClock)
}

// Mutation selects how a previously generated dataset is altered.
type Mutation string

const (
	// MutateTruncate cuts the MP4 container mid-frame, corrupting the frame
	// whose RTP packet straddles the new end of file.
	MutateTruncate Mutation = "truncate"
	// MutateReplace swaps the file content at the same path (different bytes,
	// same container/metadata revisions), forcing a content-cache miss.
	MutateReplace Mutation = "replace"
	// MutateContainer bumps only the container revision, rebuilding the index
	// while preserving the content cache.
	MutateContainer Mutation = "container"
	// MutateMetadata bumps only the metadata revision, invalidating the index
	// while preserving the content cache.
	MutateMetadata Mutation = "metadata"
)

// Mutate applies the requested change to an existing dataset under root and
// returns the updated frames. Truncate intentionally leaves the manifest
// describing the original (now missing) bytes so the loader reports the first
// locatable error instead of silently skipping a frame.
func Mutate(root string, spec Spec, frames []model.Frame, m Mutation) ([]model.Frame, error) {
	containerRel := filepath.Join(InputDir, ContainerName)
	containerPath, err := model.SafeJoin(root, containerRel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(containerPath)
	if err != nil {
		return nil, model.WrapError(model.StageWorkspace, 0, 0, "reading container for mutation", err)
	}

	switch m {
	case MutateTruncate:
		// Keep ftyp + first two RTP packets intact; cut inside packet three.
		keep, err := truncatePoint(data, frames, 3)
		if err != nil {
			return nil, err
		}
		if err = model.AtomicWrite(containerPath, data[:keep], 0o644); err != nil {
			return nil, err
		}
		return frames, nil

	case MutateReplace:
		// New payload bytes at the same path: content hash changes, revisions
		// do not. Regenerate frames with a shifted waveform phase.
		replaced := make([]model.Frame, len(frames))
		packets := make([][]byte, len(frames))
		for i := range frames {
			pcm := framePCM(spec, i+1000)
			capture := int64(i) * FrameNanos
			ts, err := rtpTimestampOf(capture)
			if err != nil {
				return nil, err
			}
			pkt, err := rtpPacket(1, uint32(i+1), ts, pcm)
			if err != nil {
				return nil, err
			}
			raw, err := pkt.Marshal()
			if err != nil {
				return nil, err
			}
			packets[i] = raw
			replaced[i] = frames[i]
			replaced[i].PayloadLength = uint32(len(pcm))
			replaced[i].SHA256 = model.Checksum(pcm)
			replaced[i].Payload = pcm
		}
		if err = writeContainer(containerPath, spec, packets); err != nil {
			return nil, err
		}
		if err = writeJSON(root, filepath.Join(InputDir, ManifestName), manifestDoc{Frames: replaced}); err != nil {
			return nil, err
		}
		return replaced, nil

	case MutateContainer:
		// Container-only change: rewrite just the ftyp revision while copying
		// the mdat byte-for-byte, so the content fingerprint stays equal and
		// only the container revision moves.
		if err = bumpContainerRevision(containerPath, data, spec.ContainerRev+1); err != nil {
			return nil, err
		}
		return frames, nil

	case MutateMetadata:
		metaPath, err := model.SafeJoin(root, filepath.Join(InputDir, MetadataName))
		if err != nil {
			return nil, err
		}
		meta := Metadata{
			MetadataVersion: spec.MetadataRev + 1,
			Generator:       "continuity/fixture",
			TrackCount:      1,
		}
		raw, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			return nil, err
		}
		if err = model.AtomicWrite(metaPath, raw, 0o644); err != nil {
			return nil, err
		}
		return frames, nil

	default:
		return nil, model.NewError(model.StageWorkspace, 0, 0, "unknown mutation: "+string(m))
	}
}

// truncatePoint returns a byte offset inside frame targetFrame's RTP packet so
// the packet is present but incomplete.
func truncatePoint(data []byte, frames []model.Frame, targetFrame int) (int, error) {
	_, mdat, err := ContainerHeader(data)
	if err != nil {
		return 0, model.WrapError(model.StageIndex, 0, 0, "parsing container header", err)
	}
	// Framed offsets are deterministic: 2-byte length prefix + RTP packet.
	off := int(mdat.Offset + mdat.HeaderSize)
	for i := range frames {
		pkt, err := packetFor(frames[i])
		if err != nil {
			return 0, err
		}
		frameSize := 2 + len(pkt)
		if i+1 == targetFrame {
			// Keep the length prefix and RTP header plus half the payload so
			// the frame is recognizable but its declared length runs past EOF.
			point := off + 2 + 12 + (len(pkt)-12)/2
			if point >= len(data) {
				point = len(data) - 1
			}
			return point, nil
		}
		off += frameSize
	}
	return 0, model.NewError(model.StageIndex, model.FrameNumber(targetFrame), int64(off), "truncation target not found")
}

func packetsFromFrames(frames []model.Frame) ([][]byte, error) {	out := make([][]byte, len(frames))
	for i := range frames {
		pkt, err := packetFor(frames[i])
		if err != nil {
			return nil, err
		}
		out[i] = pkt
	}
	return out, nil
}

func packetFor(f model.Frame) ([]byte, error) {
	ts, err := rtpTimestampOf(f.CaptureNanos)
	if err != nil {
		return nil, err
	}
	pkt, err := rtpPacket(f.Track, f.Sequence, ts, f.Payload)
	if err != nil {
		return nil, err
	}
	return pkt.Marshal()
}
