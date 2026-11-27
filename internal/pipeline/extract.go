// Package pipeline wires the EBML reader, timeline, track graph, RTP
// sequence recovery and checkpoint store into deterministic, resumable
// processing stages. Each stage is idempotent under (input hashes, params,
// stage version), so an interrupted or repeated run only redoes work whose
// inputs changed.
package pipeline

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"

	"medtrace/internal/ebml"
	"medtrace/internal/mederr"
	"medtrace/internal/rtp"
	"medtrace/internal/timeline"

	pionrtp "github.com/pion/rtp"
)

// MediaBlock is one extracted Matroska block placed on the nanosecond base.
type MediaBlock struct {
	TrackID     uint64
	BlockSeq    uint64
	TimeNs      timeline.Timestamp
	Keyframe    bool
	HeaderStart int64 // element header byte offset (for provenance)
	BodyEnd     int64 // exclusive end of the block body
}

// TrackMeta describes one declared Matroska track.
type TrackMeta struct {
	ID        uint64
	UID       uint64
	Kind      uint8
	Codec     string
	Name      string
	DependsOn []uint64
	ClockRefs []uint64
}

// MediaExtract is the single-pass media parse output (bounded memory:
// blocks are streamed into a slice sized to the fixture, never the payload).
type MediaExtract struct {
	TimecodeScale uint64
	Tracks        []TrackMeta
	Blocks        []MediaBlock
}

// ExtractMedia walks a WebM byte stream once, decoding headers and blocks.
func ExtractMedia(data []byte) (*MediaExtract, error) {
	r := ebml.NewReader(bytes.NewReader(data))
	out := &MediaExtract{TimecodeScale: 1_000_000}
	blockSeq := map[uint64]uint64{}

	err := r.WalkChildren(nil, func(top *ebml.Element) error {
		if top.ID != ebml.IDSegment {
			return r.SeekTo(top.End())
		}
		if err := r.SeekTo(top.BodyOffset); err != nil {
			return err
		}
		return r.WalkChildren(top, func(segChild *ebml.Element) error {
			switch segChild.ID {
			case ebml.IDInfo:
				return parseInfo(r, segChild, out)
			case ebml.IDTracks:
				return parseTracks(r, segChild, out)
			case ebml.IDCluster:
				return parseCluster(r, segChild, out, blockSeq)
			default:
				return r.SeekTo(segChild.End())
			}
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func parseInfo(r *ebml.Reader, info *ebml.Element, out *MediaExtract) error {
	if err := r.SeekTo(info.BodyOffset); err != nil {
		return err
	}
	return r.WalkChildren(info, func(e *ebml.Element) error {
		if e.ID == ebml.IDTimecodeScale {
			body, err := r.ReadBody(e)
			if err != nil {
				return err
			}
			out.TimecodeScale = decodeUint(body)
		}
		return r.SeekTo(e.End())
	})
}

func parseTracks(r *ebml.Reader, tracks *ebml.Element, out *MediaExtract) error {
	if err := r.SeekTo(tracks.BodyOffset); err != nil {
		return err
	}
	return r.WalkChildren(tracks, func(entry *ebml.Element) error {
		if entry.ID != ebml.IDTrackEntry {
			return r.SeekTo(entry.End())
		}
		meta := TrackMeta{}
		if err := r.SeekTo(entry.BodyOffset); err != nil {
			return err
		}
		err := r.WalkChildren(entry, func(e *ebml.Element) error {
			body, err := r.ReadBody(e)
			if err != nil {
				return err
			}
			switch e.ID {
			case ebml.IDTrackNumber:
				meta.ID = decodeUint(body)
			case ebml.IDTrackUID:
				meta.UID = decodeUint(body)
			case ebml.IDTrackType:
				meta.Kind = uint8(decodeUint(body))
			case ebml.IDCodecID:
				meta.Codec = string(body)
			case ebml.IDName:
				meta.Name = string(body)
			}
			return r.SeekTo(e.End())
		})
		if err != nil {
			return err
		}
		out.Tracks = append(out.Tracks, meta)
		return nil
	})
}

func parseCluster(r *ebml.Reader, cluster *ebml.Element, out *MediaExtract, seq map[uint64]uint64) error {
	if err := r.SeekTo(cluster.BodyOffset); err != nil {
		return err
	}
	var clusterTC int64
	// First pass needs the cluster timecode before blocks, but the elements
	// are in order per Matroska; collect blocks then resolve timestamps.
	type rawBlock struct {
		trackID     uint64
		relTC       int16
		keyframe    bool
		headerStart int64
		bodyEnd     int64
	}
	var raws []rawBlock
	err := r.WalkChildren(cluster, func(e *ebml.Element) error {
		if e.ID == ebml.IDTimecode {
			body, err := r.ReadBody(e)
			if err != nil {
				return err
			}
			clusterTC = int64(decodeUint(body))
			return r.SeekTo(e.End())
		}
		if e.ID == ebml.IDSimpleBlock {
			body, err := r.ReadBody(e)
			if err != nil {
				return err
			}
			blk, err := ebml.ParseBlock(body)
			if err != nil {
				return err
			}
			raws = append(raws, rawBlock{
				trackID:     blk.TrackNum,
				relTC:       blk.Timecode,
				keyframe:    blk.Flags&0x80 != 0,
				headerStart: e.HeaderOffset,
				bodyEnd:     e.End(),
			})
		}
		return r.SeekTo(e.End())
	})
	if err != nil {
		return err
	}
	clock, err := timeline.NewClock("webm", int64(out.TimecodeScale))
	if err != nil {
		return err
	}
	for _, rb := range raws {
		ns, err := clock.Convert(clusterTC + int64(rb.relTC))
		if err != nil {
			return err
		}
		seq[rb.trackID]++
		out.Blocks = append(out.Blocks, MediaBlock{
			TrackID:     rb.trackID,
			BlockSeq:    seq[rb.trackID],
			TimeNs:      ns,
			Keyframe:    rb.keyframe,
			HeaderStart: rb.headerStart,
			BodyEnd:     rb.bodyEnd,
		})
	}
	return nil
}

func decodeUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = (v << 8) | uint64(c)
	}
	return v
}

// eventLine mirrors the fixture JSONL record.
type eventLine struct {
	ArrivalNs int64  `json:"arrival_ns"`
	SSRC      uint32 `json:"ssrc"`
	Sequence  uint16 `json:"sequence"`
	RawHex    string `json:"rtp_hex"`
}

// AnalyzeEvents parses JSONL RTP records and runs sequence recovery with the
// given window. Reading is line-oriented so memory stays bounded.
func AnalyzeEvents(path string, windowNs int64) ([]rtp.Stats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, mederr.New(mederr.CatIO, "open events %q", path).Wrap(err)
	}
	defer f.Close()

	analyzer, err := rtp.NewAnalyzer(windowNs)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(f)
	line := 0
	for dec.More() {
		line++
		var ev eventLine
		if err := dec.Decode(&ev); err != nil {
			return nil, mederr.New(mederr.CatInvalidInput, "events line %d is not valid JSON", line).Wrap(err)
		}
		raw, err := hex.DecodeString(ev.RawHex)
		if err != nil {
			return nil, mederr.New(mederr.CatInvalidInput, "events line %d has invalid rtp_hex", line).Wrap(err)
		}
		pkt := &pionrtp.Packet{}
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, mederr.New(mederr.CatInvalidInput, "events line %d is not an RTP packet", line).Wrap(err)
		}
		if pkt.SSRC != ev.SSRC || pkt.SequenceNumber != ev.Sequence {
			return nil, mederr.New(mederr.CatInvalidInput,
				"events line %d header/record mismatch", line)
		}
		if err := analyzer.AddPacket(timeline.Timestamp(ev.ArrivalNs), pkt); err != nil {
			return nil, err
		}
	}
	return analyzer.Flush(), nil
}
