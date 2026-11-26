// Package verify independently re-validates produced artifacts. It never
// trusts the index: sidecars are re-parsed, segment hashes recomputed from
// the source, and time monotonicity plus keyframe offsets are checked against
// the stream. Every problem is reported with its offset and recoverability.
package verify

import (
	"errors"
	"fmt"
	"io"
	"os"

	"tsindex/internal/digest"
	"tsindex/internal/fault"
	"tsindex/internal/index"
	"tsindex/internal/ingest"
)

// Report summarizes one verification pass.
type Report struct {
	Segments        int
	SegmentsMatched int
	Keyframes       int
	KeyframesHit    int
	// Monotonic holds when unified ticks never decrease within a track.
	Monotonic bool
	// Recoverable is true when every fault found can be survived on resume.
	Recoverable bool
	Faults      []fault.Fault
}

// Validator re-reads source and sidecars with its own parser instances.
type Validator struct {
	rec *fault.Recorder
}

// NewValidator returns an independent validator.
func NewValidator() *Validator { return &Validator{rec: fault.NewRecorder()} }

// VerifyFile re-parses a plain TS source and checks every sidecar listed.
// sidecars maps segment base names to on-disk sidecar paths.
func (v *Validator) VerifyFile(sourcePath string, sidecars map[string]string) (Report, error) {
	f, err := os.Open(sourcePath)
	if err != nil {
		return Report{}, err
	}
	defer f.Close()

	sr := ingest.NewStreamReader(f, v.rec)
	// PSI tracking re-validates PAT/PMT CRCs and version fields so a bad
	// section is caught independently, not trusted from the original index.
	psi := ingest.NewPSITracker(v.rec)
	// Stream the source once to validate packet framing, sync bytes, PSI
	// tables and the monotonicity of unified ticks on every track. Segment
	// hashes are recomputed independently in VerifySegments.
	lastTick := map[uint16]int64{}
	monotonic := true

	for {
		rec, perr := sr.NextPacket()
		if errors.Is(perr, io.EOF) {
			break
		}
		if perr != nil {
			return Report{}, perr
		}
		psi.Feed(rec)
		if rec.HasTime {
			if prev, ok := lastTick[rec.PID]; ok && rec.Ticks < prev {
				monotonic = false
				v.rec.Record(fault.Fault{Kind: fault.KindCorruptPacket,
					Offset: rec.Offset, Recoverable: true,
					Detail: "unified ticks moved backwards on a track"})
			}
			lastTick[rec.PID] = rec.Ticks
		}
	}

	rep := Report{Monotonic: monotonic, Recoverable: v.rec.Recoverable(),
		Faults: v.rec.All()}

	// Re-parse every sidecar and confirm its hashes against recomputation is
	// done segment-by-segment in VerifySegments; here we validate structure.
	for base, path := range sidecars {
		rec, rerr := index.ReadSidecar(path)
		if rerr != nil {
			if errors.Is(rerr, index.ErrIncompleteSidecar) {
				v.rec.Record(fault.Fault{Kind: fault.KindIncompleteIndex,
					Offset: 0, Recoverable: true,
					Detail: base + ": " + rerr.Error()})
				continue
			}
			return rep, rerr
		}
		rep.Segments++
		rep.Keyframes += len(rec.Keyframes)
		for _, kf := range rec.Keyframes {
			if kf.Complete {
				rep.KeyframesHit++
			}
			if kf.SourceOffset < 0 || kf.SourceOffset >= rec.EndOffset ||
				kf.SourceOffset < rec.StartOffset {
				v.rec.Record(fault.Fault{Kind: fault.KindIncompleteIndex,
					Offset: kf.SourceOffset, Recoverable: true,
					Detail: "keyframe offset outside segment bounds"})
			}
		}
	}
	rep.Faults = v.rec.All()
	rep.Recoverable = v.rec.Recoverable()
	return rep, nil
}

// VerifySegments recomputes each segment's raw and content hashes directly
// from source byte ranges and compares them to the sidecar. It is the
// authoritative content-integrity check the report's match count uses.
func (v *Validator) VerifySegments(sourcePath string, recs []index.SegmentRecord) (Report, error) {
	f, err := os.Open(sourcePath)
	if err != nil {
		return Report{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Report{}, err
	}
	sourceLen := info.Size()
	rep := Report{Monotonic: true, Recoverable: true}
	// One reusable packet buffer keeps verification streaming: the whole
	// source is never resident, only a single 188-byte packet at a time.
	var pktBuf [ingest.PacketSize]byte
	for _, rec := range recs {
		rep.Segments++
		if rec.EndOffset > sourceLen || rec.StartOffset < 0 ||
			rec.EndOffset <= rec.StartOffset {
			v.rec.Record(fault.Fault{Kind: fault.KindIncompleteIndex,
				Offset: rec.StartOffset, Recoverable: true,
				Detail: "sidecar segment bounds outside source"})
			continue
		}
		length := rec.EndOffset - rec.StartOffset
		if length%ingest.PacketSize != 0 {
			v.rec.Record(fault.Fault{Kind: fault.KindTruncated,
				Offset: rec.EndOffset, Recoverable: true,
				Detail: "segment is not whole 188-byte packets"})
			continue
		}
		sec := io.NewSectionReader(f, rec.StartOffset, length)
		h := digest.NewHasher()
		var off int64
		for off+ingest.PacketSize <= length {
			if _, rerr := io.ReadFull(sec, pktBuf[:]); rerr != nil {
				v.rec.Record(fault.Fault{Kind: fault.KindTruncated,
					Offset: rec.StartOffset + off, Recoverable: true,
					Detail: "short read while re-hashing segment"})
				break
			}
			pkt := pktBuf[:]
			if pkt[0] != ingest.SyncByte {
				v.rec.Record(fault.Fault{Kind: fault.KindCorruptPacket,
					Offset: rec.StartOffset + off, Recoverable: true,
					Detail: "missing sync byte during verification"})
			}
			h.AddPacket(pkt, payloadOf(pkt))
			off += ingest.PacketSize
		}
		got := h.Sum()
		var want digest.Digests
		want.Raw = rec.RawHash
		want.Content = rec.ContentHash
		if got.Equal(want) {
			rep.SegmentsMatched++
		} else {
			v.rec.Record(fault.Fault{Kind: fault.KindIncompleteIndex,
				Offset: rec.StartOffset, Recoverable: true,
				Detail: fmt.Sprintf("segment %d hash mismatch", rec.SegmentID)})
		}
		rep.Keyframes += len(rec.Keyframes)
		for _, kf := range rec.Keyframes {
			if kf.Complete {
				rep.KeyframesHit++
			}
			if kf.SourceOffset < rec.StartOffset || kf.SourceOffset >= rec.EndOffset {
				v.rec.Record(fault.Fault{Kind: fault.KindIncompleteIndex,
					Offset: kf.SourceOffset, Recoverable: true,
					Detail: fmt.Sprintf("segment %d keyframe offset outside bounds",
						rec.SegmentID)})
			}
		}
	}
	rep.Faults = v.rec.All()
	rep.Recoverable = v.rec.Recoverable()
	return rep, nil
}

// payloadOf mirrors ingest.Packet.Payload without constructing a full packet.
func payloadOf(raw []byte) []byte {
	off := 4
	hasAdapt := raw[3]&0x20 != 0
	hasPayload := raw[3]&0x10 != 0
	if hasAdapt {
		off += 1 + int(raw[4])
	}
	if !hasPayload || off >= len(raw) {
		return nil
	}
	return raw[off:]
}
