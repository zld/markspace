// Package index defines the scan artifact shared by the validator, the
// streaming runner and the reporter.
//
// An Index is a flat, serialisable description of every track, sample and
// fragment found in one MP4 asset, plus the RTP packet events observed for
// its audio track. It is bound to the exact input bytes through a digest, so
// a stale index can be rejected without trusting cached state.
package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
)

// FormatVersion is bumped whenever the on-disk Index layout changes.
const FormatVersion = 1

const (
	// InputDigestAlgorithm names the input digest algorithm and domain
	// separation version.
	InputDigestAlgorithm = "sha256/v1"
	// ReportDigestAlgorithm names the stable report summary algorithm.
	ReportDigestAlgorithm = "fnv1a64/v1"
)

// Digest is a labelled hash over input bytes.
type Digest struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
	Size      int64  `json:"size"`
}

// Input records one digested input stream.
type Input struct {
	Name   string `json:"name"`
	Digest Digest `json:"digest"`
}

// Sample is one decodable (or subtitle) sample located in the MP4 input.
type Sample struct {
	TrackID      uint32      `json:"track"`
	Index        int         `json:"index"`
	Offset       int64       `json:"offset"`
	Size         uint32      `json:"size"`
	Ticks        uint64      `json:"ticks"`
	TickDuration uint32      `json:"tick_duration"`
	DTS          clock.Nanos `json:"dts"`
	Duration     clock.Nanos `json:"duration"`
	Fragment     int         `json:"fragment"`
}

// Track is a container track and its samples in decode order.
type Track struct {
	ID        uint32      `json:"id"`
	Kind      string      `json:"kind"`
	Handler   string      `json:"handler"`
	Codec     string      `json:"codec"`
	Timescale uint32      `json:"timescale"`
	Start     clock.Nanos `json:"start"`
	End       clock.Nanos `json:"end"`
	Samples   []Sample    `json:"samples"`
}

// Fragment is one moof and the samples attributed to it.
type Fragment struct {
	Ordinal  int         `json:"ordinal"`
	Sequence uint32      `json:"sequence"`
	Offset   int64       `json:"offset"`
	Size     int64       `json:"size"`
	Start    clock.Nanos `json:"start"`
	End      clock.Nanos `json:"end"`
	Tracks   []uint32    `json:"tracks"`
	Samples  int         `json:"samples"`
}

// PacketKind classifies an RTP event on the delivery timeline.
type PacketKind string

const (
	// PacketOK is a deliverable packet.
	PacketOK PacketKind = "packet"
	// PacketDuplicate is a retransmission: it does not advance media time.
	PacketDuplicate PacketKind = "duplicate"
	// PacketGap marks missing sequence numbers. No sample is fabricated.
	PacketGap PacketKind = "gap"
)

// Packet is one RTP delivery event. GapCount is set only for PacketGap.
type Packet struct {
	Kind          PacketKind  `json:"kind"`
	SeqExt        uint64      `json:"seq_ext"`
	Seq           uint16      `json:"seq"`
	Timestamp     uint64      `json:"timestamp"`
	Time          clock.Nanos `json:"time"`
	Offset        int64       `json:"offset"`
	PayloadOffset int64       `json:"payload_offset"`
	PayloadSize   int         `json:"payload_size"`
	GapCount      uint64      `json:"gap_count,omitempty"`
}

// Index is the complete scan artifact. Faults are in-memory only: an index
// that has faults is never marked Complete and is therefore never reused.
type Index struct {
	Version   int            `json:"version"`
	Digest    Digest         `json:"digest"`
	Inputs    []Input        `json:"inputs"`
	Complete  bool           `json:"complete"`
	Tracks    []Track        `json:"tracks"`
	Fragments []Fragment     `json:"fragments"`
	Packets   []Packet       `json:"packets"`
	RTPTrack  uint32         `json:"rtp_track"`
	ClockRate uint32         `json:"clock_rate"`
	Faults    []*fault.Error `json:"-"`
}

// AudioTrack returns the first audio track, or nil.
func (idx *Index) AudioTrack() *Track {
	for i := range idx.Tracks {
		if idx.Tracks[i].Kind == "audio" {
			return &idx.Tracks[i]
		}
	}
	return nil
}

// TrackByID returns the track with the given ID, or nil.
func (idx *Index) TrackByID(id uint32) *Track {
	for i := range idx.Tracks {
		if idx.Tracks[i].ID == id {
			return &idx.Tracks[i]
		}
	}
	return nil
}

func hashBytes(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest{Algorithm: InputDigestAlgorithm, Value: hex.EncodeToString(sum[:]), Size: int64(len(b))}
}

// ComputeDigest binds both input streams to a single dependency digest.
func ComputeDigest(mp4, rtp []byte) (Digest, []Input) {
	h := sha256.New()
	h.Write([]byte("assetcheck/input/v1\x00"))
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(mp4)))
	h.Write(lenBuf[:])
	h.Write(mp4)
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(rtp)))
	h.Write(lenBuf[:])
	h.Write(rtp)
	combined := Digest{
		Algorithm: InputDigestAlgorithm,
		Value:     hex.EncodeToString(h.Sum(nil)),
		Size:      int64(len(mp4)) + int64(len(rtp)),
	}
	inputs := []Input{
		{Name: "asset.mp4", Digest: hashBytes(mp4)},
		{Name: "packets.rtp", Digest: hashBytes(rtp)},
	}
	return combined, inputs
}

// Save persists the index atomically.
func (idx *Index) Save(path string) error {
	if idx == nil {
		return errors.New("index: nothing to save")
	}
	data, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("index: marshal: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("index: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("index: commit: %w", err)
	}
	return nil
}

// Load reads a persisted index and checks its internal structure.
func Load(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("index: decode: %w", err)
	}
	if err := idx.CheckStructure(); err != nil {
		return nil, err
	}
	return &idx, nil
}

// CheckStructure verifies that the index is internally complete, without
// consulting the input bytes. Callers must still compare Digest.
func (idx *Index) CheckStructure() error {
	if idx.Version != FormatVersion {
		return fmt.Errorf("index: unsupported version %d", idx.Version)
	}
	if idx.Digest.Algorithm != InputDigestAlgorithm || len(idx.Digest.Value) != 64 {
		return errors.New("index: missing or invalid input digest")
	}
	if !idx.Complete {
		return errors.New("index: scan did not complete")
	}
	if len(idx.Tracks) == 0 {
		return errors.New("index: no tracks")
	}
	seen := make(map[uint32]bool, len(idx.Tracks))
	for ti := range idx.Tracks {
		tr := &idx.Tracks[ti]
		if seen[tr.ID] {
			return fmt.Errorf("index: duplicate track id %d", tr.ID)
		}
		seen[tr.ID] = true
		if tr.Timescale == 0 {
			return fmt.Errorf("index: track %d has zero timescale", tr.ID)
		}
		if len(tr.Samples) == 0 {
			return fmt.Errorf("index: track %d has no samples", tr.ID)
		}
		for si := range tr.Samples {
			s := &tr.Samples[si]
			if s.TrackID != tr.ID || s.Index != si {
				return fmt.Errorf("index: track %d sample %d is mislabelled", tr.ID, si)
			}
			if s.Offset < 0 || s.Size == 0 || s.Duration <= 0 {
				return fmt.Errorf("index: track %d sample %d has invalid extent", tr.ID, si)
			}
			if s.Fragment < 0 || s.Fragment >= len(idx.Fragments) {
				return fmt.Errorf("index: track %d sample %d has invalid fragment", tr.ID, si)
			}
		}
	}
	for fi := range idx.Fragments {
		if idx.Fragments[fi].Ordinal != fi {
			return fmt.Errorf("index: fragment %d has wrong ordinal", fi)
		}
	}
	counts := make(map[int]int, len(idx.Fragments))
	for ti := range idx.Tracks {
		for si := range idx.Tracks[ti].Samples {
			counts[idx.Tracks[ti].Samples[si].Fragment]++
		}
	}
	for fi := range idx.Fragments {
		if counts[fi] != idx.Fragments[fi].Samples {
			return fmt.Errorf("index: fragment %d sample count mismatch", fi)
		}
	}
	var lastExt uint64
	for pi := range idx.Packets {
		p := &idx.Packets[pi]
		if p.Offset < 0 {
			return fmt.Errorf("index: packet %d has invalid offset", pi)
		}
		switch p.Kind {
		case PacketOK:
			if p.PayloadOffset < 0 || p.PayloadSize < 0 {
				return fmt.Errorf("index: packet %d has invalid payload", pi)
			}
			if p.Time == clock.Invalid {
				return fmt.Errorf("index: packet %d has no time", pi)
			}
		case PacketDuplicate:
			if p.Time == clock.Invalid {
				return fmt.Errorf("index: duplicate %d has no time", pi)
			}
		case PacketGap:
			if p.GapCount == 0 {
				return fmt.Errorf("index: gap %d has no count", pi)
			}
		default:
			return fmt.Errorf("index: packet %d has unknown kind %q", pi, p.Kind)
		}
		if pi > 0 && p.SeqExt < lastExt {
			return fmt.Errorf("index: packet %d sequence regresses", pi)
		}
		lastExt = p.SeqExt
	}
	return nil
}
