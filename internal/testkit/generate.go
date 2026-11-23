package testkit

import (
	"fmt"

	"mediad/internal/ebml"
)

// DefaultDocSpec returns a two-track 40 s document at 25 fps / 50 Hz.
func DefaultDocSpec() DocSpec {
	return DocSpec{
		DocType:          "matroska",
		ScaleNs:          1_000_000,
		SegmentUnknown:   true,
		ClusterUnknown:   true,
		ClipDurMs:        40_000,
		BlocksPerCluster: 25,
		Tracks: []TrackSpec{
			{Number: 1, Codec: "V_VP9", Name: "camera-a", Language: "und",
				FrameDurMs: 40, FrameBytes: 900, KeyEvery: 25},
			{Number: 2, Codec: "A_OPUS", Name: "boom-1", Language: "und",
				FrameDurMs: 20, FrameBytes: 160},
		},
	}
}

// GeneratedBlock records a block's intended properties, so tests can assert
// against ground truth rather than re-deriving it.
type GeneratedBlock struct {
	Track      uint64
	Index      int // per-track block index
	TimeMs     uint32
	Keyframe   bool
	PayloadLen int
	Sequence   int // global emission order
}

// GeneratedDoc is a serialized document plus ground truth.
type GeneratedDoc struct {
	Data   []byte
	Spec   DocSpec
	Blocks []GeneratedBlock
	Events int
	Root   *Element
	// TrackType maps track number to EBML TrackType (1 video, 2 audio).
	TrackType map[uint64]uint64
}

// Durations returns the per-track frame duration in milliseconds.
func (s DocSpec) duration(t TrackSpec) int {
	if t.FrameDurMs <= 0 {
		return 20
	}
	return t.FrameDurMs
}

func (s DocSpec) blocksPerCluster() int {
	if s.BlocksPerCluster <= 0 {
		return 20
	}
	return s.BlocksPerCluster
}

// BuildDoc creates the synthetic document.
func BuildDoc(spec DocSpec) (*GeneratedDoc, error) {
	if len(spec.Tracks) == 0 {
		return nil, fmt.Errorf("testkit: no tracks")
	}
	if spec.ScaleNs <= 0 {
		spec.ScaleNs = ebml.DefaultTimecodeScaleNs
	}
	if spec.ClipDurMs <= 0 {
		spec.ClipDurMs = 40_000
	}
	rng := NewRNG(spec.Seed)

	info := Master(ebml.IDInfo,
		Uint(ebml.IDTimecodeScale, uint64(spec.ScaleNs)),
		Str(ebml.IDMuxingApp, "mediad-testkit"),
		Str(ebml.IDWritingApp, "mediad-testkit"),
		Float(ebml.IDDuration, float64(spec.ClipDurMs)),
	)
	var trackEntries []*Element
	typeMap := map[uint64]uint64{}
	for _, t := range spec.Tracks {
		tt := uint64(0)
		switch t.Type {
		case TrackTypeVideo:
			tt = 1
		case TrackTypeAudio:
			tt = 2
		default:
			// Infer from codec prefix when unspecified.
			if len(t.Codec) > 0 && t.Codec[0] == 'V' {
				tt = 1
			} else {
				tt = 2
			}
		}
		typeMap[t.Number] = tt
		entry := Master(ebml.IDTrackEntry,
			Uint(ebml.IDTrackNumber, t.Number),
			Uint(ebml.IDTrackUID, t.Number*7919),
			Uint(ebml.IDTrackType, tt),
			Str(ebml.IDCodecID, t.Codec),
			Str(ebml.IDName, t.Name),
			Str(ebml.IDLanguage, t.Language),
			Uint(ebml.IDDefaultDur, uint64(spec.duration(t))*uint64(spec.ScaleNs)),
		)
		if tt == 1 {
			entry.Children = append(entry.Children, Master(ebml.IDVideo,
				Uint(ebml.IDPixelWidth, 1920), Uint(ebml.IDPixelHeight, 1080)))
		} else {
			entry.Children = append(entry.Children, Master(ebml.IDAudio,
				Float(ebml.IDSamplingFreq, 48000), Uint(ebml.IDChannels, 2)))
		}
		trackEntries = append(trackEntries, entry)
	}

	seg := Master(ebml.IDSegment,
		Master(ebml.IDSeekHead),
		info,
		Master(ebml.IDTracks, trackEntries...),
	)

	// Emit blocks interleaved by time.
	var pending []pendingBlock
	for _, t := range spec.Tracks {
		dur := spec.duration(t)
		n := spec.ClipDurMs / dur
		for i := 0; i < n; i++ {
			dx := float64(i*dur) * (1 + float64(t.DriftPPM)/1e6)
			// True time orders the file; the stored timecode is the 32-bit
			// counter, which wraps. A recording made across the wrap is
			// therefore ordered by true time but carries a wrapping counter,
			// exactly as a real muxer would produce it.
			trueMs := int64(t.StartOffsetMs) + int64(dx)
			tm := uint32(trueMs)
			key := false
			if typeMap[t.Number] == 1 && t.KeyEvery > 0 {
				key = i%t.KeyEvery == 0
			}
			if typeMap[t.Number] == 1 && t.KeyEvery == 0 {
				key = true
			}
			if t.SparseEvery > 0 && typeMap[t.Number] == 2 && i%t.SparseEvery == t.SparseEvery-1 {
				continue
			}
			payload := rng.Bytes(t.FrameBytes)
			pending = append(pending, pendingBlock{
				track: t, idx: i, timeMs: tm, trueMs: trueMs, key: key, payload: payload,
			})
		}
	}
	stableSortBlocks(pending)

	var blocks []GeneratedBlock
	var cluster *Element
	perCluster := spec.blocksPerCluster()
	inCluster := 0
	clusterTrueMs := int64(0)
	flushCluster := func() {
		if cluster != nil {
			seg.Children = append(seg.Children, cluster)
			cluster = nil
		}
	}
	newCluster := func(pb pendingBlock) (*Element, int64) {
		c := Master(ebml.IDCluster)
		c.UnknownSize = spec.ClusterUnknown
		c.Children = append(c.Children, Uint(ebml.IDTimecode, uint64(pb.timeMs)))
		return c, pb.trueMs
	}
	seq := 0
	for _, pb := range pending {
		if cluster == nil || inCluster >= perCluster || pb.trueMs < clusterTrueMs {
			flushCluster()
			cluster, clusterTrueMs = newCluster(pb)
			inCluster = 0
		}
		// Block offsets are relative to the cluster's stored timecode. That
		// difference is computed on unwrapped time, so a cluster that
		// straddles the counter wrap still yields a small signed offset.
		offset := pb.trueMs - clusterTrueMs
		if offset < -32768 || offset > 32767 {
			flushCluster()
			cluster, clusterTrueMs = newCluster(pb)
			inCluster = 0
			offset = 0
		}
		// Track number VINT: marker bit set, then the value.
		vint := EncodeVINT(pb.track.Number)
		body := append([]byte{}, vint...)
		body = append(body, byte(int16(offset)>>8), byte(int16(offset)))
		flags := byte(0)
		if pb.key {
			flags |= 0x80
		}
		body = append(body, flags)
		body = append(body, pb.payload...)
		cluster.Children = append(cluster.Children, Bytes(ebml.IDSimpleBlock, body))
		blocks = append(blocks, GeneratedBlock{
			Track: pb.track.Number, Index: pb.idx, TimeMs: pb.timeMs,
			Keyframe: pb.key, PayloadLen: len(pb.payload), Sequence: seq,
		})
		seq++
		inCluster++
	}
	flushCluster()

	root := Master(ebml.IDEBML, Str(ebml.IDDocType, spec.DocType))
	seg.UnknownSize = spec.SegmentUnknown
	root.Children = append(root.Children, seg)
	data, err := Serialize(root)
	if err != nil {
		return nil, err
	}
	return &GeneratedDoc{
		Data:      data,
		Spec:      spec,
		Blocks:    blocks,
		Root:      root,
		TrackType: typeMap,
	}, nil
}

// EncodeVINT encodes v as an EBML VINT (size form) in the shortest width.
func EncodeVINT(v uint64) []byte {
	return SizeBytes(v)
}
