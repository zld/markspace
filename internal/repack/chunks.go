package repack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediaflow/internal/index"
	"mediaflow/internal/policy"
	"mediaflow/internal/probe"
)

func chunkPath(dir string, i int, temp bool) string {
	name := fmt.Sprintf("chunk-%06d.mchk", i)
	if temp {
		name += ".tmp"
	}
	return filepath.Join(dir, name)
}

func removeChunkArtifacts(dir string, i int) error {
	for _, p := range []string{chunkPath(dir, i, false), chunkPath(dir, i, true)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func verifyChunkFile(path string, rec index.ChunkRecord) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return false, err
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	return n == rec.ByteLength && digest == rec.SHA256, nil
}

func writeChunk(input *os.File, r *probe.Report, rec index.ChunkRecord, dir string, hooks Hooks) (int64, *index.ChunkRecord, error) {
	track := r.FindTrack(rec.TrackID)
	if track == nil {
		return 0, nil, fmt.Errorf("track %d not found for chunk %d", rec.TrackID, rec.Index)
	}
	if rec.StartSample < 0 || rec.EndSample > len(track.Samples) || rec.StartSample >= rec.EndSample {
		return 0, nil, fmt.Errorf("invalid sample range %d..%d", rec.StartSample, rec.EndSample)
	}
	var payloadBytes int64
	for i := rec.StartSample; i < rec.EndSample; i++ {
		payloadBytes += int64(track.Samples[i].Size)
	}

	tmpPath := chunkPath(dir, rec.Index, true)
	finalPath := chunkPath(dir, rec.Index, false)
	out, err := os.Create(tmpPath)
	if err != nil {
		return 0, nil, err
	}
	h := sha256.New()
	w := io.MultiWriter(out, h)
	if _, err := w.Write(boxHeader("mchk", 8+payloadBytes)); err != nil {
		out.Close()
		return 0, nil, err
	}

	var written int64
	for i := rec.StartSample; i < rec.EndSample; i++ {
		sample := track.Samples[i]
		if _, err := input.Seek(sample.Offset, io.SeekStart); err != nil {
			out.Close()
			return 0, nil, err
		}
		n, err := io.CopyN(w, input, int64(sample.Size))
		if err != nil {
			out.Close()
			return 0, nil, err
		}
		written += n
		if i == rec.StartSample && hooks.AfterFirstSample != nil {
			if err := hooks.AfterFirstSample(rec.Index, tmpPath); err != nil {
				out.Close()
				return written, nil, err
			}
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return 0, nil, err
	}
	if err := out.Close(); err != nil {
		return 0, nil, err
	}
	sum := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return 0, nil, err
	}
	info, err := os.Stat(finalPath)
	if err != nil {
		return 0, nil, err
	}
	rec.ByteLength = info.Size()
	rec.SHA256 = sum
	return written, &rec, nil
}

func actualKbps(r *probe.Report, outputBytes int64) int {
	if r == nil {
		return 0
	}
	return int(policy.BitrateKbps(outputBytes, r.Stats.DurationMS))
}

func boxHeader(typ string, size int64) []byte {
	b := make([]byte, 8)
	b[0] = byte(uint32(size) >> 24)
	b[1] = byte(uint32(size) >> 16)
	b[2] = byte(uint32(size) >> 8)
	b[3] = byte(uint32(size))
	copy(b[4:8], typ)
	return b
}
