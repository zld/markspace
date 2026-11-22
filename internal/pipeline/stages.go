package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"mediapipe/internal/cache"
	"mediapipe/internal/sync"
)

// Stage cache payloads. Each stage writes a small, deterministic record keyed
// by the digest of its own inputs plus the processing version, so a hit can
// only be served for exactly the same upstream state.
type transportPayload struct {
	Segment uint32         `json:"segment"`
	Stage   TransportStage `json:"stage"`
	Proofs  []GroupProof   `json:"proofs"`
	Inputs  string         `json:"inputs"`
}

type containerPayload struct {
	Segment uint32         `json:"segment"`
	Stage   ContainerStage `json:"stage"`
	Inputs  string         `json:"inputs"`
}

type syncPayload struct {
	Segment uint32    `json:"segment"`
	Stage   SyncStage `json:"stage"`
	Inputs  string    `json:"inputs"`
}

// stageKey derives a stage address from the previous stage's address, the
// stage name, the segment and the processing version.
func stageKey(input cache.Key, stage string, seq uint32) cache.Key {
	var b [4]byte
	b[0], b[1], b[2], b[3] = byte(seq>>24), byte(seq>>16), byte(seq>>8), byte(seq)
	return cache.DigestParts([]byte(input), []byte(stage), []byte(Version), b[:])
}

// stageIO bundles the provenance and counters of one stage access.
type stageIO struct {
	hits     int
	rejects  int
	key      cache.Key
	verified bool
}

// getStage verifies a cached stage result. A miss is silent; a mismatch is
// counted and the entry is quarantined by the store.
func (r *Runner) getStage(key cache.Key, stage string, in cache.Source, dst any) (*stageIO, bool, error) {
	io := &stageIO{key: key}
	got, err := r.store.Fetch(cache.Lookup{Key: key, Stage: stage, Version: Version, Input: in})
	if err != nil {
		if errors.Is(err, cache.ErrMissing) {
			return io, false, nil
		}
		r.rejects++
		io.rejects++
		return io, false, nil
	}
	// The store has already proven that the payload hashes to the manifest's
	// declared content digest; the entry address is input-derived, so it is
	// compared separately by the caller's payload identity checks.
	if cache.KeyFromBytes(got.Bytes) != got.Manifest.Content {
		r.rejects++
		io.rejects++
		return io, false, nil
	}
	if err := json.Unmarshal(got.Bytes, dst); err != nil {
		r.rejects++
		io.rejects++
		return io, false, nil
	}
	r.hits++
	io.hits++
	io.verified = true
	return io, true, nil
}

// stageHitCounter exposes how many stage-level hits occurred, without the
// segment-level index hits. It is used by tests that assert each stage is
// genuinely cached rather than only the final index.
func (r *Runner) stageHits() int {
	return r.hits - r.indexHits
}

// putStage stores a stage result under its input-derived address. The
// manifest carries the content digest of the stored bytes separately, so a
// tampered payload is detected even though the address itself is not derived
// from the output.
func (r *Runner) putStage(stage string, addr cache.Key, body []byte, in cache.Source, locator string) (cache.Key, error) {
	content := cache.KeyFromBytes(body)
	manifest := cache.Manifest{
		Format: "mediapipe-cache/1", Stage: stage, Version: Version, Key: addr,
		Input: in,
		ResultSource: cache.Source{ID: pipelineSourceID, Digest: content,
			Bytes: int64(len(body)), Locator: locator},
	}
	if err := r.store.Store(manifest, body); err != nil {
		return "", err
	}
	return addr, nil
}

// stageSource describes a stage output as the input of the next stage.
func stageSource(stage string, key cache.Key, size int, locator string) cache.Source {
	return cache.Source{ID: pipelineSourceID + "/" + stage, Digest: key, Bytes: int64(size), Locator: locator}
}

// runTransportStage performs the transport stage for one segment: it verifies
// every reassembled group against the scenario's authoritative digest and
// reports the missing groups as explicit state.
func (r *Runner) runTransportStage(seq uint32, groupIdx []int, tr *transportOutcome) (*transportPayload, cache.Key, error) {
	key := stageKey(r.InputKey(), StageTransport, seq)
	var cached transportPayload
	if _, ok, _ := r.getStage(key, StageTransport, r.source(), &cached); ok && cached.Segment == seq {
		return &cached, key, nil
	}
	out := &transportPayload{Segment: seq, Inputs: r.InputKey().String()}
	if w, ok := tr.windows[seq]; ok {
		out.Stage.SeqStart = w.Start
		out.Stage.SeqEnd = w.End
		out.Stage.SeqCount = w.Count
	}
	missingSet := map[int]bool{}
	for _, k := range tr.missing {
		g := r.groupFor(k)
		if g != nil && g.Segment == seq {
			missingSet[g.Index] = true
		}
	}
	for _, gi := range groupIdx {
		g := r.scenario.Groups[gi]
		if g.TrackID == 3 {
			continue
		}
		out.Stage.PacketsIn += g.FragCount
	}
	for _, gi := range groupIdx {
		g := r.scenario.Groups[gi]
		if missingSet[gi] {
			out.Stage.MissingGroups = append(out.Stage.MissingGroups, gi)
			out.Stage.MissingPackets += g.FragCount
			continue
		}
		data, ok := tr.groups.groupData(g.TrackID, g.Frame)
		if !ok {
			out.Stage.MissingGroups = append(out.Stage.MissingGroups, gi)
			out.Stage.MissingPackets += g.FragCount
			continue
		}
		out.Stage.PacketsOut += g.FragCount
		digest := sha256.Sum256(data)
		proof := GroupProof{Group: gi, TrackID: g.TrackID, Frame: g.Frame, Kind: g.Kind,
			Digest: hex.EncodeToString(digest[:]), Source: hex.EncodeToString(g.Digest[:])}
		proof.Match = digest == g.Digest
		out.Proofs = append(out.Proofs, proof)
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	addr, err := r.putStage(StageTransport, key, body, r.source(), fmt.Sprintf("segment:%d:transport", seq))
	if err != nil {
		return nil, "", err
	}
	return out, addr, nil
}

// runContainerStage verifies sample ownership and metadata round trips.
func (r *Runner) runContainerStage(seq uint32, transport *transportPayload, transportKey cache.Key) (*containerPayload, cache.Key, error) {
	in := stageSource(StageTransport, transportKey, 0, fmt.Sprintf("segment:%d:transport", seq))
	key := stageKey(transportKey, StageContainer, seq)
	var cached containerPayload
	if _, ok, _ := r.getStage(key, StageContainer, in, &cached); ok && cached.Segment == seq {
		return &cached, key, nil
	}
	out := &containerPayload{Segment: seq, Inputs: transportKey.String()}
	for _, proof := range transport.Proofs {
		if !proof.Match {
			out.Stage.Mismatched++
			continue
		}
		if proof.Kind != "meta" {
			out.Stage.Verified++
			out.Stage.Samples++
		}
	}
	if idx := segmentIndex(r.index, seq); idx >= 0 {
		entry := r.index.Segments[idx]
		out.Stage.SegmentHash = entry.HashHex()
		out.Stage.Keyframes = len(entry.KeyframeSeqs)
		out.Stage.Samples = entry.SampleCount
	}
	for _, rec := range r.records {
		if metaSegment(rec, r.records) != seq {
			continue
		}
		out.Stage.MetadataRecs++
		if err := verifyRecord(rec); err == nil {
			out.Stage.MetadataOK++
		}
	}
	if out.Stage.Mismatched > 0 {
		return out, "", fmt.Errorf("%w: segment %d %d payload mismatches", ErrInvariant, seq, out.Stage.Mismatched)
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	addr, err := r.putStage(StageContainer, key, body, in, fmt.Sprintf("segment:%d:container", seq))
	if err != nil {
		return nil, "", err
	}
	return out, addr, nil
}

// runSyncStage aligns the segment's tracks on the unified line.
func (r *Runner) runSyncStage(seq uint32, containerKey cache.Key, tr *transportOutcome) (*SyncStage, cache.Key, error) {
	in := stageSource(StageContainer, containerKey, 0, fmt.Sprintf("segment:%d:container", seq))
	key := stageKey(containerKey, StageSync, seq)
	var cached syncPayload
	if _, ok, _ := r.getStage(key, StageSync, in, &cached); ok && cached.Segment == seq {
		return &cached.Stage, key, nil
	}
	views, _, err := r.views(seq, tr)
	if err != nil {
		return nil, "", err
	}
	var stage SyncStage
	if len(views) == 2 {
		stage.VideoSamples = len(views[0].Samples)
		stage.AudioSamples = len(views[1].Samples)
		align, aerr := sync.Align(views[0], views[1], sync.AlignConfig{ToleranceTicks: 4500, BaseUncertainty: 2})
		if aerr != nil && !errors.Is(aerr, sync.ErrMissingState) && !errors.Is(aerr, sync.ErrNonMonotonic) {
			return nil, "", aerr
		}
		stage.OffsetTicks = align.Offset
		stage.Uncertainty = align.Uncertainty
		stage.Aligned = align.Status == "aligned"
		for _, d := range align.Diagnostics {
			if d.Code == "align_outlier" {
				stage.NonMonotonic++
			}
		}
		if aerr != nil && errors.Is(aerr, sync.ErrMissingState) {
			stage.Aligned = false
		}
	}
	stage.LayoutIssues = len(r.layout[seq])
	body, err := json.Marshal(syncPayload{Segment: seq, Stage: stage, Inputs: containerKey.String()})
	if err != nil {
		return nil, "", err
	}
	addr, err := r.putStage(StageSync, key, body, in, fmt.Sprintf("segment:%d:sync", seq))
	if err != nil {
		return nil, "", err
	}
	return &stage, addr, nil
}

// stageBytes is the size helper used for provenance byte counts.
func stageBytes(v any) int {
	b, _ := json.Marshal(v)
	return len(b)
}
