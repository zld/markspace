// Package recover scans an input's media area for essence frame boundaries
// when the container index is missing, unusable or points at bytes that are
// no longer inside the file. It rebuilds an explicit timeline from the frame
// headers themselves and reports every interval it could not determine
// instead of silently closing the gap.
package recover

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"archive/internal/essence"
	"archive/internal/fault"
	"archive/internal/model"
	"archive/internal/probe"
)

// FrameState classifies a scanned candidate.
type FrameState string

const (
	// StateComplete is a fully present, checksum-valid frame.
	StateComplete FrameState = "complete"
	// StateDamaged is a frame whose header is intact but whose payload fails
	// validation. Its time boundary is known; its media is not recoverable.
	StateDamaged FrameState = "damaged"
	// StatePartial is a frame whose declared size runs past the end of the
	// available bytes. Its boundary is known from the header, its media is
	// truncated.
	StatePartial FrameState = "partial"
)

// Frame is one recovered essence frame.
type Frame struct {
	TrackKey  string       `json:"track_key"`
	TrackID   uint32       `json:"track_id"`
	Kind      essence.Kind `json:"kind"`
	Seq       uint32       `json:"seq"`
	State     FrameState   `json:"state"`
	Offset    uint64       `json:"offset"` // file offset of the frame header
	Wrapper   uint64       `json:"wrapper_offset"`
	Size      uint64       `json:"size"`      // bytes the header declares
	Available uint64       `json:"available"` // bytes actually present
	StartTick uint64       `json:"start_tick"`
	Duration  uint64       `json:"duration"`
	Sync      bool         `json:"sync"`
	SHA256    string       `json:"sha256,omitempty"`

	Payload []byte `json:"-"`
}

// EndTick is the exclusive end on the track clock.
func (f *Frame) EndTick() uint64 { return f.StartTick + f.Duration }

// TrackScan is the scan outcome for one track.
type TrackScan struct {
	Key       string          `json:"key"`
	TrackID   uint32          `json:"track_id"`
	Kind      model.TrackKind `json:"kind"`
	Timebase  model.Timebase  `json:"timebase"`
	Frames    []*Frame        `json:"frames"`
	StartTick uint64          `json:"start_tick"`
	EndTick   uint64          `json:"end_tick"`
	Complete  int             `json:"complete"`
	Damaged   int             `json:"damaged"`
	Partial   int             `json:"partial"`
	// Gaps are tick intervals inside the scanned extent where no frame
	// header could be found at all. They are always reported.
	Gaps []model.NanosRange `json:"gaps,omitempty"`
}

// Result is the recovery outcome for a container.
type Result struct {
	Container model.ContainerKind `json:"container"`
	SHA256    string              `json:"sha256"`
	Tracks    []*TrackScan        `json:"tracks"`
	Faults    fault.List          `json:"-"`
	Scanned   bool                `json:"scanned"`
}

// ByKey returns one track's scan.
func (r *Result) ByKey(key string) *TrackScan {
	for _, t := range r.Tracks {
		if t.Key == key {
			return t
		}
	}
	return nil
}

// Plan decides which tracks need scanning. A track with a complete index
// whose fragments all land inside the file is trusted; anything else is
// scanned so a truncated or lying index cannot drop recoverable media.
func Plan(f *probe.File) (need []*model.Track, trusted []*model.Track) {
	for _, t := range f.Tracks {
		if t.Index.Complete && len(t.Fragments) > 0 && fragmentsUsable(f, t) {
			trusted = append(trusted, t)
		} else {
			need = append(need, t)
		}
	}
	return need, trusted
}

// FragmentsUsable reports whether every indexed fragment lies inside the file.
func fragmentsUsable(f *probe.File, t *model.Track) bool {
	for _, fr := range t.Fragments {
		if fr.PayloadOffset+fr.Size > f.Size {
			return false
		}
	}
	return true
}

// ScanTrack scans the whole input for frames belonging to one track id.
//
// Resynchronisation works on the frame's own 4-byte magic, which is unique
// by construction; every hit is then fully validated (version, header CRC,
// payload CRC, track id), so an accidental match inside a payload cannot
// become a frame. For MXF the 20-byte KLV wrapper immediately before a frame
// is recognised and folded into the reported offsets.
func ScanTrack(data []byte, t *model.Track) (*TrackScan, fault.List) {
	scan := &TrackScan{Key: t.Key, TrackID: t.TrackID, Kind: t.Kind, Timebase: t.Timebase}
	var faults fault.List
	magic := essence.Magic[:]
	off := 0
	for off+essence.MinFrameSize <= len(data) {
		idx := indexMagic(data[off:], magic)
		if idx < 0 {
			break
		}
		pos := off + idx
		h, err := essence.ParseHeader(data[pos:])
		if err != nil || h.TrackID != t.TrackID {
			off = pos + 1
			continue
		}
		frame := &Frame{
			TrackKey: t.Key, TrackID: h.TrackID, Kind: h.Kind, Seq: h.Seq,
			Offset: uint64(pos), Size: h.TotalSize(), StartTick: h.StartTick,
			Duration: h.Duration, Sync: h.Sync, Wrapper: uint64(pos),
		}
		// Fold in the KLV wrapper when present, so offsets are comparable
		// with the probe's fragment convention.
		if pos >= 20 && isKLVKey(data[pos-20:pos]) {
			frame.Wrapper = uint64(pos - 20)
		}
		avail := uint64(len(data) - pos)
		total := h.TotalSize()
		if total <= avail {
			frame.Available = total
			payload := data[pos : pos+int(total)]
			if ok, complete := essence.VerifyPayload(payload, h); complete && ok {
				frame.State = StateComplete
				frame.Payload = payload
				sum := sha256.Sum256(payload)
				frame.SHA256 = hex.EncodeToString(sum[:])
				scan.Complete++
			} else {
				frame.State = StateDamaged
				faults.Add(fault.New(fault.CorruptSegment, frame.Wrapper,
					"frame %d of %s fails payload checksum", h.Seq, t.Key).
					WithTrack(t.Key, t.TrackID).
					WithRange(t.Timebase.TickToNanos(h.StartTick), t.Timebase.TickToNanos(h.StartTick+h.Duration)).
					WithRecoverable(false))
				scan.Damaged++
			}
			scan.Frames = append(scan.Frames, frame)
			off = pos + int(total)
			continue
		}
		// The declared frame is cut by EOF: its start boundary is known, its
		// media is not.
		frame.Available = avail
		frame.State = StatePartial
		faults.Add(fault.New(fault.TruncatedBox, frame.Wrapper,
			"frame %d of %s declares %d bytes, %d present", h.Seq, t.Key, total, avail).
			WithTrack(t.Key, t.TrackID).
			WithRange(t.Timebase.TickToNanos(h.StartTick), t.Timebase.TickToNanos(h.StartTick+h.Duration)).
			WithRecoverable(false))
		scan.Partial++
		scan.Frames = append(scan.Frames, frame)
		// Nothing after a cut frame can be trusted to start on a boundary.
		break
	}

	sort.SliceStable(scan.Frames, func(i, j int) bool {
		if scan.Frames[i].StartTick != scan.Frames[j].StartTick {
			return scan.Frames[i].StartTick < scan.Frames[j].StartTick
		}
		return scan.Frames[i].Offset < scan.Frames[j].Offset
	})
	computeExtent(scan, t.Timebase)
	return scan, faults
}

// computeExtent derives the start/end and the internal gaps of a scan. A gap
// is a tick interval between two consecutive scanned frames that no frame
// covers; those intervals are media that could not be localized.
func computeExtent(scan *TrackScan, tb model.Timebase) {
	if len(scan.Frames) == 0 {
		return
	}
	scan.StartTick = scan.Frames[0].StartTick
	scan.EndTick = scan.StartTick
	prevEnd := scan.StartTick
	for i, fr := range scan.Frames {
		if i > 0 && fr.StartTick > prevEnd {
			scan.Gaps = append(scan.Gaps, model.NanosRange{
				StartNS: tb.TickToNanos(prevEnd),
				EndNS:   tb.TickToNanos(fr.StartTick),
			})
		}
		if fr.EndTick() > scan.EndTick {
			scan.EndTick = fr.EndTick()
		}
		if fr.EndTick() > prevEnd {
			prevEnd = fr.EndTick()
		}
	}
}

func indexMagic(b, magic []byte) int {
	// A thin wrapper keeps the dependency surface obvious and lets tests
	// substitute a slower search if needed.
	return bytes.Index(b, magic)
}

// isKLVKey reports whether b looks like the 20-byte KLV prefix the fixture
// writes (16-byte key then a 0x83 BER length).
func isKLVKey(b []byte) bool {
	if len(b) < 20 {
		return false
	}
	if b[16] != 0x83 {
		return false
	}
	return b[0] == 0x06 && b[1] == 0x0E && b[2] == 0x2B && b[3] == 0x34
}

// Rebuild turns a scan into an explicit timeline. Complete frames contribute
// recovered segments; damaged and partial frames contribute unrecovered
// segments with a known boundary; internal gaps are unrecovered intervals.
func Rebuild(scan *TrackScan, report *model.TrackTimeline) {
	if scan == nil || report == nil {
		return
	}
	report.Key = scan.Key
	report.TrackID = scan.TrackID
	report.Timebase = scan.Timebase
	report.Rebuilt = true
	report.Complete = scan.Damaged == 0 && scan.Partial == 0 && len(scan.Gaps) == 0 && len(scan.Frames) > 0
	report.UnrecoveredNS = append(report.UnrecoveredNS, scan.Gaps...)
	if len(scan.Frames) == 0 {
		report.Complete = false
		return
	}
	report.Declared = model.NanosRange{
		StartNS: scan.Timebase.TickToNanos(scan.StartTick),
		EndNS:   scan.Timebase.TickToNanos(scan.EndTick),
	}
	report.Recovered = report.Declared
	for _, fr := range scan.Frames {
		seg := model.Segment{
			TrackKey:  scan.Key,
			Source:    "scan",
			StartTick: fr.StartTick,
			Duration:  fr.Duration,
			StartNS:   scan.Timebase.TickToNanos(fr.StartTick),
			EndNS:     scan.Timebase.TickToNanos(fr.EndTick()),
			Recovered: fr.State == StateComplete,
		}
		if fr.State != StateComplete {
			seg.Unrecovered = true
			seg.Reason = string(fr.State)
			report.UnrecoveredNS = append(report.UnrecoveredNS, model.NanosRange{
				StartNS: seg.StartNS, EndNS: seg.EndNS,
			})
		}
		report.Segments = append(report.Segments, seg)
	}
}

// LocalizationError expresses a reported instant relative to the truth in
// input ticks. Complete and damaged frames are reported at their own header
// boundary, so the value is 0; it exists so reports state the guarantee and
// tests can assert it.
func LocalizationError(tb model.Timebase, reportedNS, truthNS int64) uint64 {
	return tb.TickError(reportedNS - truthNS)
}

// Summary renders a compact terminal line for a scan.
func Summary(s *TrackScan) string {
	if s == nil {
		return "scan=none"
	}
	return fmt.Sprintf("scan track=%s complete=%d damaged=%d partial=%d gaps=%d start_ns=%d end_ns=%d",
		s.Key, s.Complete, s.Damaged, s.Partial, len(s.Gaps),
		s.Timebase.TickToNanos(s.StartTick), s.Timebase.TickToNanos(s.EndTick))
}
