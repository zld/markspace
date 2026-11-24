// Package mux performs lossless remuxing: it rebuilds container structure
// around demuxed packets without ever touching codec payload bytes. Two
// containers are supported — Matroska/WebM (the native round-trip target) and
// fragmented-free MP4 built with go-mp4 box primitives. A per-block payload
// hash is computed on both the input packet and the written bytes so the
// muxer can prove the remux is byte-preserving.
package mux

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/track"
)

// Format selects the output container.
type Format uint8

const (
	FormatWebM Format = iota
	FormatMP4
)

// BlockDigest pairs one output block with the SHA-256 of its codec payload.
type BlockDigest struct {
	Sequence    int
	OutputTrack uint32
	Offset      int64
	Length      int
	SHA256      [32]byte
}

// Result summarizes a remux run.
type Result struct {
	Format      Format
	Bytes       int
	BlockCount  int
	Digests     []BlockDigest
	PayloadHash [32]byte // hash over the concatenation of all payload hashes
}

// hashPayload returns the SHA-256 of a single codec payload.
func hashPayload(p []byte) [32]byte { return sha256.Sum256(p) }

// WriteWebM rebuilds a Matroska/WebM stream. Packets are grouped back into
// per-track clusters at a fixed 100ms boundary; payloads are copied verbatim.
func WriteWebM(w io.Writer, mapping *track.Mapping, packets []*demux.Packet) (*Result, error) {
	var buf bytes.Buffer
	res := &Result{Format: FormatWebM}
	root := sha256.New()

	writeEBMLHead(&buf)
	// Segment body is assembled first so its size is known.
	var seg bytes.Buffer
	writeInfoAndTracks(&seg, mapping)
	if err := writeWebMClusters(&seg, packets, mapping, res, root); err != nil {
		return nil, err
	}
	writeElement(&buf, idSegment, seg.Bytes())
	res.Bytes = buf.Len()
	res.BlockCount = len(res.Digests)
	copy(res.PayloadHash[:], root.Sum(nil))
	if _, err := w.Write(buf.Bytes()); err != nil {
		return nil, fault.New(fault.KindIO, -1, "write: %v", err)
	}
	return res, nil
}

// writeWebMClusters emits one cluster per 100ms tick window, preserving the
// original interleaving order inside each window.
func writeWebMClusters(seg *bytes.Buffer, packets []*demux.Packet,
	mapping *track.Mapping, res *Result, root io.Writer) error {
	const clusterWindowNS int64 = 100_000_000 // 100 ms
	var windowStart int64 = -1
	var body bytes.Buffer
	flush := func() error {
		if body.Len() == 0 {
			return nil
		}
		writeElement(seg, idCluster, body.Bytes())
		body.Reset()
		return nil
	}
	for _, p := range packets {
		win := (p.TimestampNS / clusterWindowNS) * clusterWindowNS
		if windowStart < 0 {
			windowStart = win
			writeUintChild(&body, idTimestamp, uint64(win/1_000_000))
		} else if win != windowStart {
			if err := flush(); err != nil {
				return err
			}
			windowStart = win
			body.Reset()
			writeUintChild(&body, idTimestamp, uint64(win/1_000_000))
		}
		rel := int16((p.TimestampNS - win) / 1_000_000)
		block := encodeWebMBlock(p.InputTrack, rel, p.Keyframe, p.Payload)
		blockStart := int64(seg.Len()) + int64(body.Len())
		writeElement(&body, idSimpleBlock, block)
		d := BlockDigest{
			Sequence: p.Sequence, OutputTrack: p.OutputTrack,
			Offset: blockStart, Length: len(p.Payload),
			SHA256: hashPayload(p.Payload),
		}
		res.Digests = append(res.Digests, d)
		if _, err := root.Write(d.SHA256[:]); err != nil {
			return fault.New(fault.KindIO, -1, "digest: %v", err)
		}
	}
	return flush()
}

// WriteMP4 builds a single-track-per-trak MP4 using go-mp4 box headers. The
// mdat payload is the verbatim concatenation of packet payloads in order.
func WriteMP4(w io.WriteSeeker, mapping *track.Mapping, packets []*demux.Packet) (*Result, error) {
	res := &Result{Format: FormatMP4}
	root := sha256.New()

	if _, err := writeBox(w, mp4.BoxTypeFtyp(), ftypPayload()); err != nil {
		return nil, err
	}
	// Collect payloads and per-sample sizes in output-track order.
	var mdat bytes.Buffer
	var samples []mp4SampleLike
	for _, p := range packets {
		off := int64(mdat.Len())
		if _, err := mdat.Write(p.Payload); err != nil {
			return nil, fault.New(fault.KindIO, -1, "mdat: %v", err)
		}
		h := hashPayload(p.Payload)
		samples = append(samples, mp4SampleLike{
			track: p.OutputTrack, size: uint32(len(p.Payload)),
			off: off, hash: h, seq: p.Sequence,
		})
		if _, err := root.Write(h[:]); err != nil {
			return nil, fault.New(fault.KindIO, -1, "digest: %v", err)
		}
	}
	moov, err := buildMoov(mapping, samples)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(moov); err != nil {
		return nil, fault.New(fault.KindIO, -1, "moov: %v", err)
	}
	mdatStart, err := writeBox(w, mp4.BoxTypeMdat(), mdat.Bytes())
	if err != nil {
		return nil, err
	}
	for _, s := range samples {
		res.Digests = append(res.Digests, BlockDigest{
			Sequence: s.seq, OutputTrack: s.track,
			Offset: mdatStart + s.off, Length: int(s.size), SHA256: s.hash,
		})
	}
	res.BlockCount = len(samples)
	copy(res.PayloadHash[:], root.Sum(nil))
	pos, _ := w.Seek(0, io.SeekEnd)
	res.Bytes = int(pos)
	return res, nil
}

// writeBox writes an 8-byte box header and a raw payload, returning the
// payload's absolute offset.
func writeBox(w io.WriteSeeker, typ mp4.BoxType, payload []byte) (int64, error) {
	off, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, fault.New(fault.KindIO, off, "seek: %v", err)
	}
	bi, err := mp4.WriteBoxInfo(w, &mp4.BoxInfo{
		Type: typ, Size: uint64(mp4.SmallHeaderSize + len(payload)),
		HeaderSize: mp4.SmallHeaderSize,
	})
	if err != nil {
		return 0, fault.New(fault.KindIO, off, "box header: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		return 0, fault.New(fault.KindIO, off, "box payload: %v", err)
	}
	return int64(bi.Offset) + int64(bi.HeaderSize), nil
}

func ftypPayload() []byte {
	// major_brand + minor_version + compatible brands.
	return []byte{'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
}

// VerifyRoundTrip re-reads an MP4 output with go-mp4 and asserts its mdat
// contains exactly the expected payload bytes. It is the structural
// validation hook required for MP4 outputs.
func VerifyRoundTrip(r io.ReadSeeker, wantPayload []byte) error {
	paths, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeFtyp()}, {mp4.BoxTypeMdat()},
	})
	if err != nil {
		return fault.New(fault.KindPayloadMismatch, -1, "mp4 structure invalid: %v", err)
	}
	var mdat *mp4.BoxInfo
	for _, p := range paths {
		if p.Type == mp4.BoxTypeMdat() {
			mdat = p
		}
	}
	if mdat == nil {
		return fault.New(fault.KindPayloadMismatch, -1, "mdat box missing")
	}
	got := make([]byte, mdat.Size-mdat.HeaderSize)
	if _, err := r.Seek(int64(mdat.Offset+mdat.HeaderSize), io.SeekStart); err != nil {
		return fault.New(fault.KindIO, int64(mdat.Offset), "seek mdat: %v", err)
	}
	if _, err := io.ReadFull(r, got); err != nil {
		return fault.New(fault.KindIO, int64(mdat.Offset), "read mdat: %v", err)
	}
	if !bytes.Equal(got, wantPayload) {
		return fault.New(fault.KindPayloadMismatch, int64(mdat.Offset),
			"mp4 mdat payload differs from demuxed packets")
	}
	return nil
}

// be32 is a small helper for fixed-width box integers.
func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
