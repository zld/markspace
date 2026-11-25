// Package derive renders each segment into a content-addressed clip blob,
// streaming source bytes and reusing cached results when the inputs match.
package derive

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"replaycheck/internal/model"
	"replaycheck/internal/policy"
	"replaycheck/internal/split"
	"replaycheck/internal/store"
)

// ClipFormatVersion is mixed into every cache key and clip header, so a format
// change can never reuse an older artifact.
const ClipFormatVersion = "rclip1"

// clipHeader is embedded at the start of a derived artifact. Its JSON encoding
// is deterministic because it is a fixed struct with no maps.
type clipHeader struct {
	Format       string     `json:"format"`
	Segment      int        `json:"segment"`
	StartTick    int64      `json:"startTick"`
	EndTick      int64      `json:"endTick"`
	Level        string     `json:"level"`
	QualityPPM   policy.PPM `json:"qualityPpm"`
	SourceDigest string     `json:"sourceDigest"`
	PolicyDigest string     `json:"policyDigest"`
	Trigger      string     `json:"trigger"`
}

// Clip is one derived artifact.
type Clip struct {
	Segment    int        `json:"segment"`
	Bytes      int64      `json:"bytes"`
	Digest     string     `json:"digest"`
	Cached     bool       `json:"cached"`
	Level      string     `json:"level"`
	QualityPPM policy.PPM `json:"qualityPpm"`
	Truncated  bool       `json:"truncated"`
}

// Deriver streams source sample ranges into content-addressed clips. It never
// reads more than one bounded buffer at a time and never mutates the source.
type Deriver struct {
	Source       *os.File
	SourceDigest string
	Blobs        *store.BlobStore
	Cache        *store.Derived
	PolicyDigest string
	// Buffer is the streaming chunk size; zero selects 64 KiB.
	Buffer int
}

type clipMeta struct {
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

// Derive produces one clip per segment at the chosen level. The clip is a
// deterministic header followed by up to the policy's payload allowance of
// source sample bytes. Truncation stands in for transcoding: it is bounded by
// the level's byte ratio and is recorded, never silent.
func (d *Deriver) Derive(plan *split.Plan, res *policy.Result) ([]Clip, error) {
	if d.Source == nil || d.Blobs == nil || d.Cache == nil {
		return nil, model.NewError(model.StageDerive, "derive", model.TrackRef{}, 0, "deriver is not fully configured")
	}
	if len(plan.Segments) != len(res.Choices) {
		return nil, model.Errorf(model.StageDerive, "derive", model.TrackRef{}, 0,
			"segment count %d != choice count %d", len(plan.Segments), len(res.Choices))
	}
	clips := make([]Clip, 0, len(plan.Segments))
	for i, seg := range plan.Segments {
		choice := res.Choices[i]
		key, err := d.cacheKey(seg, choice)
		if err != nil {
			return nil, err
		}
		if raw, ok, err := d.Cache.Get(key); err != nil {
			return nil, err
		} else if ok {
			var meta clipMeta
			if err := json.Unmarshal(raw, &meta); err == nil && model.ValidDigest(meta.Digest) {
				if size, serr := d.Blobs.Size(meta.Digest); serr == nil && size == meta.Bytes {
					clips = append(clips, Clip{
						Segment: seg.Index, Bytes: meta.Bytes, Digest: meta.Digest, Cached: true,
						Level: choice.Level, QualityPPM: choice.QualityPPM,
					})
					continue
				}
			}
		}
		clip, err := d.deriveOne(seg, choice)
		if err != nil {
			return nil, err
		}
		if err := d.Cache.Put(key, clipMeta{Digest: clip.Digest, Bytes: clip.Bytes}); err != nil {
			return nil, err
		}
		clips = append(clips, clip)
	}
	return clips, nil
}

func (d *Deriver) cacheKey(seg split.Segment, choice policy.Choice) (string, error) {
	ranges, err := model.DigestJSON(seg.Ranges)
	if err != nil {
		return "", model.WrapError(model.StageDerive, "cache", model.TrackRef{}, 0, "cannot digest ranges", err)
	}
	return store.Key(ClipFormatVersion, d.SourceDigest, d.PolicyDigest,
		fmt.Sprint(seg.Index), fmt.Sprint(seg.Span), choice.Level, ranges), nil
}

func (d *Deriver) deriveOne(seg split.Segment, choice policy.Choice) (Clip, error) {
	if choice.Bytes <= 0 {
		return Clip{}, model.Errorf(model.StageDerive, "derive", model.TrackRef{}, 0, "segment %d has no byte allowance", seg.Index)
	}
	header, err := json.Marshal(clipHeader{
		Format: ClipFormatVersion, Segment: seg.Index,
		StartTick: int64(seg.Span.Start), EndTick: int64(seg.Span.End),
		Level: choice.Level, QualityPPM: choice.QualityPPM,
		SourceDigest: d.SourceDigest, PolicyDigest: d.PolicyDigest, Trigger: seg.StartName,
	})
	if err != nil {
		return Clip{}, model.WrapError(model.StageDerive, "derive", model.TrackRef{}, 0, "cannot encode clip header", err)
	}
	prefix := make([]byte, 8+4)
	copy(prefix, "RCLIP1\x00\x00")
	binary.BigEndian.PutUint32(prefix[8:], uint32(len(header)))
	// The first bytes are magic + header length, then the JSON header.
	lead := append(prefix, header...)
	allowance := choice.Bytes - int64(len(lead))
	if allowance < 0 {
		return Clip{}, model.Errorf(model.StageDerive, "derive", model.TrackRef{}, uint64(choice.Bytes),
			"segment %d allowance %d is smaller than its %d-byte header", seg.Index, choice.Bytes, len(lead))
	}
	payload, total, err := d.streamPayload(seg, allowance)
	if err != nil {
		return Clip{}, err
	}
	// Padding keeps the artifact exactly the allowance requested by policy,
	// so the manifest total equals the negotiated budget.
	pad := allowance - int64(len(payload))
	if pad > 0 {
		payload = append(payload, make([]byte, pad)...)
	}
	digest, size, err := d.Blobs.Put(io.MultiReader(bytes.NewReader(lead), bytes.NewReader(payload)))
	if err != nil {
		return Clip{}, err
	}
	return Clip{
		Segment: seg.Index, Bytes: size, Digest: digest,
		Level: choice.Level, QualityPPM: choice.QualityPPM,
		Truncated: total < int64(seg.Bytes),
	}, nil
}

// streamPayload reads the segment's source ranges in order until allowance
// bytes are collected, using a single reusable buffer.
func (d *Deriver) streamPayload(seg split.Segment, allowance int64) ([]byte, int64, error) {
	bufSize := d.Buffer
	if bufSize <= 0 {
		bufSize = 64 << 10
	}
	buf := make([]byte, bufSize)
	out := make([]byte, 0, allowance)
	var total int64
	for _, r := range seg.Ranges {
		if total >= allowance {
			break
		}
		remaining := allowance - total
		limit := int64(r.Size)
		if limit > remaining {
			limit = remaining
		}
		if _, err := d.Source.Seek(int64(r.Offset), io.SeekStart); err != nil {
			return nil, 0, model.WrapError(model.StageDerive, "read", model.TrackRef{ID: r.TrackID, Kind: "trak"}, r.Offset, "cannot seek source", err)
		}
		toRead := limit
		for toRead > 0 {
			n := int64(len(buf))
			if n > toRead {
				n = toRead
			}
			got, err := io.ReadFull(d.Source, buf[:n])
			if err != nil {
				return nil, 0, model.WrapError(model.StageDerive, "read", model.TrackRef{ID: r.TrackID, Kind: "trak"}, r.Offset, "short read from sealed source", err)
			}
			out = append(out, buf[:got]...)
			toRead -= int64(got)
			total += int64(got)
		}
	}
	return out, total, nil
}
