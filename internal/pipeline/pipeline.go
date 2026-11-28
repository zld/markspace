// Package pipeline wires the container, RTP, stage and merge subsystems into
// the resumable scan -> rtp -> merge flow used by the CLI adapter.
//
// Stage artifacts are canonical JSON documents; their bytes are content
// addressed by the stage store, so re-executing a stage with the same inputs
// reproduces the identical artifact and the same downstream manifest.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"rtptrace/internal/container"
	"rtptrace/internal/fault"
	"rtptrace/internal/merge"
	"rtptrace/internal/rtp"
	"rtptrace/internal/stage"
)

// Artifact relative paths under the run root.
const (
	ScanArtifact    = "artifacts/scan.json"
	RTPArtifact     = "artifacts/rtp.json"
	ManifestName    = "manifest.json"
	ArtifactBinName = "artifact.bin"
)

// ScanResult is the scan stage's persisted payload.
type ScanResult struct {
	InputDigest string             `json:"input_digest"`
	FileSize    uint64             `json:"file_size"`
	Tracks      []ScanTrackSummary `json:"tracks"`
}

// ScanTrackSummary captures the parsed samples needed downstream.
type ScanTrackSummary struct {
	Index     int               `json:"index"`
	TrackID   uint32            `json:"track_id"`
	Timescale uint32            `json:"timescale"`
	Samples   []container.Sample `json:"samples"`
}

// RTPResult is the rtp stage's persisted payload.
type RTPResult struct {
	InputDigest string            `json:"input_digest"`
	Seed        int64             `json:"seed"`
	Stats       rtp.Stats         `json:"stats"`
	Gaps        []rtp.Gap         `json:"gaps"`
	Packets     []merge.PacketRow `json:"packets"`
}

// Config parameterizes one full pipeline run.
type Config struct {
	InputPath      string
	Seed           int64
	WindowCapacity int
	ReorderEvery   int
	DuplicateEvery int
	BurstLoss      []int
	StartSeq       uint16
	InterruptAfter string
}

// Executors builds the three stage executors sharing one config. The input
// file is re-read inside the scan executor so the first stage is self
// contained after a restart.
func Executors(cfg Config) map[string]stage.Executor {
	return map[string]stage.Executor{
		stage.NameScan:  scanExecutor(cfg),
		stage.NameRTP:   rtpExecutor(cfg),
		stage.NameMerge: mergeExecutor(cfg),
	}
}

func scanExecutor(cfg Config) stage.Executor {
	return func(ctx *stage.RunContext) (string, []byte, error) {
		c, err := container.ScanFile(cfg.InputPath)
		if err != nil {
			return "", nil, err
		}
		res := ScanResult{InputDigest: c.SHA256, FileSize: c.FileSize}
		for _, tr := range c.Tracks {
			res.Tracks = append(res.Tracks, ScanTrackSummary{
				Index: tr.Index, TrackID: tr.TrackID,
				Timescale: tr.Timescale, Samples: tr.Samples,
			})
		}
		data, err := writeCanonical(res)
		if err != nil {
			return "", nil, err
		}
		return ScanArtifact, data, nil
	}
}

func rtpExecutor(cfg Config) stage.Executor {
	return func(ctx *stage.RunContext) (string, []byte, error) {
		scanBytes, err := ctx.ReadUpstream(stage.NameScan)
		if err != nil {
			return "", nil, err
		}
		var scan ScanResult
		if err := json.Unmarshal(scanBytes, &scan); err != nil {
			return "", nil, fault.Newf(fault.KindCorrupt, stage.NameScan, fault.NoOffset,
				"scan artifact unreadable: %v", err)
		}

		// Rebuild the container view the RTP builder expects from the scan
		// artifact, avoiding any dependency on the original file at this stage.
		c := rebuiltContainer(scan)
		ordered, err := rtp.BuildPackets(c)
		if err != nil {
			return "", nil, err
		}
		traceCfg := rtp.TraceConfig{
			WindowCapacity: cfg.WindowCapacity,
			ReorderEvery:   cfg.ReorderEvery,
			DuplicateEvery: cfg.DuplicateEvery,
			BurstLoss:      cfg.BurstLoss,
			StartSeq:       cfg.StartSeq,
		}
		delivered := rtp.Simulate(ordered, traceCfg, cfg.Seed)
		emitted, stats, gaps, err := rtp.Reassemble(delivered, cfg.WindowCapacity)
		if err != nil {
			return "", nil, err
		}

		// Map each emitted packet back to its sample row via sequence index.
		sampleByTrack := map[int][]container.Sample{}
		for _, t := range scan.Tracks {
			sampleByTrack[t.Index] = t.Samples
		}
		rows := make([]merge.PacketRow, 0, len(emitted))
		for _, p := range emitted {
			track := int(p.SSRC) - 0x4000
			idx := int(p.SequenceNumber) - int(cfg.StartSeq)
			samples := sampleByTrack[track]
			if idx < 0 || idx >= len(samples) {
				return "", nil, fault.Newf(fault.KindInternal, "rtp.row", fault.NoOffset,
					"seq %d maps outside track %d samples", p.SequenceNumber, track).WithSeq(p.SequenceNumber)
			}
			s := samples[idx]
			rows = append(rows, merge.PacketRow{
				TrackIndex:   track,
				SSRC:         p.SSRC,
				Sequence:     p.SequenceNumber,
				RTPTimestamp: p.Timestamp,
				MediaDTS:     s.DTS,
				Offset:       s.Offset,
				Size:         s.Size,
				Payload:      p.Payload,
			})
		}

		res := RTPResult{
			InputDigest: scan.InputDigest,
			Seed:        cfg.Seed,
			Stats:       stats,
			Gaps:        gaps,
			Packets:     rows,
		}
		data, err := writeCanonical(res)
		if err != nil {
			return "", nil, err
		}
		return RTPArtifact, data, nil
	}
}

func mergeExecutor(cfg Config) stage.Executor {
	return func(ctx *stage.RunContext) (string, []byte, error) {
		rtpBytes, err := ctx.ReadUpstream(stage.NameRTP)
		if err != nil {
			return "", nil, err
		}
		var res RTPResult
		if err := json.Unmarshal(rtpBytes, &res); err != nil {
			return "", nil, fault.Newf(fault.KindCorrupt, stage.NameRTP, fault.NoOffset,
				"rtp artifact unreadable: %v", err)
		}
		mStats := merge.Stats{
			Received:         res.Stats.Received,
			DuplicatePackets: res.Stats.Duplicates,
			LostPackets:      res.Stats.LostPackets,
			ReorderedPackets: res.Stats.Reordered,
			EmittedPackets:   res.Stats.Emitted,
		}
		manifest, artifact, err := merge.Build(res.InputDigest, res.Seed, res.Packets, mStats)
		if err != nil {
			return "", nil, err
		}
		// The merge stage commits the manifest; artifact.bin is written
		// alongside it by the CLI after commit, using the embedded hash.
		manifestBytes, err := merge.EncodeManifest(manifest)
		if err != nil {
			return "", nil, err
		}
		// Stash the artifact bytes via a sidecar record inside the manifest
		// stage result is undesirable; instead persist a bundle.
		bundle := struct {
			Manifest []byte `json:"-"`
			Artifact []byte `json:"-"`
			Digest   string `json:"digest"`
		}{Manifest: manifestBytes, Artifact: artifact, Digest: manifest.ArtifactSHA256}
		data, err := writeCanonical(struct {
			Digest   string `json:"artifact_sha256"`
			Manifest string `json:"manifest"`
			Artifact string `json:"artifact_hex"`
		}{
			Digest:   bundle.Digest,
			Manifest: string(manifestBytes),
			Artifact: hex.EncodeToString(artifact),
		})
		if err != nil {
			return "", nil, err
		}
		return stage.NameMerge + ".json", data, nil
	}
}

// rebuiltContainer reconstructs the minimal container view for BuildPackets.
func rebuiltContainer(scan ScanResult) *container.Container {
	c := &container.Container{SHA256: scan.InputDigest, FileSize: scan.FileSize}
	for _, t := range scan.Tracks {
		c.Tracks = append(c.Tracks, &container.Track{
			Index: t.Index, TrackID: t.TrackID,
			Timescale: t.Timescale, Samples: t.Samples, SampleCount: len(t.Samples),
		})
	}
	return c
}

func writeCanonical(v interface{}) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fault.Newf(fault.KindInternal, "canonical", fault.NoOffset, "marshal: %v", err)
	}
	return append(data, '\n'), nil
}

// DigestBytes is a small helper reused by the CLI for independent verification.
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
