package fixture

import (
	"fmt"
	"sort"
	"strings"

	"archive/internal/essence"
	"archive/internal/model"
)

// DamageKind names a fault that the demo injects into a generated copy.
type DamageKind string

const (
	// DamageTruncate cuts the file in the middle of the last frame.
	DamageTruncate DamageKind = "truncate"
	// DamageSegment overwrites a contiguous run of payload bytes.
	DamageSegment DamageKind = "segment"
	// DamageIndex omits every index/sample-table so recovery must scan.
	DamageIndex DamageKind = "index"
	// DamageLength rewrites a declared box/KLV length so it no longer
	// matches the bytes actually present.
	DamageLength DamageKind = "length"
)

// Injection records where damage was actually placed. Tests assert that a
// reported fault range covers the injected interval and that its offset is
// within one input tick of the injected one.
type Injection struct {
	Kind      DamageKind `json:"kind"`
	Track     string     `json:"track,omitempty"`
	TrackID   uint32     `json:"track_id"`
	Offset    uint64     `json:"offset_bytes"`
	Length    uint64     `json:"length_bytes"`
	StartTick uint64     `json:"start_tick"`
	EndTick   uint64     `json:"end_tick"`
	StartNS   int64      `json:"start_ns"`
	EndNS     int64      `json:"end_ns"`
	// Timescale is the injected track's clock, so a consumer can convert
	// the reported nanosecond error back into input ticks.
	Timescale uint32 `json:"timescale"`
	HasRange  bool   `json:"has_range"`
}

// ParseDamageKinds parses the --corrupt flag (comma separated names).
func ParseDamageKinds(s string) ([]DamageKind, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	seen := map[DamageKind]bool{}
	var out []DamageKind
	for _, part := range strings.Split(s, ",") {
		kind := DamageKind(strings.TrimSpace(part))
		switch kind {
		case DamageTruncate, DamageSegment, DamageIndex, DamageLength:
		default:
			return nil, fmt.Errorf("fixture: unknown damage %q", part)
		}
		if !seen[kind] {
			seen[kind] = true
			out = append(out, kind)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// DamagePlan is one concrete injection request.
type DamagePlan struct {
	Kind  DamageKind
	Track string
	At    uint64
	Len   uint64
}

// Plan builds the concrete byte-patch injections for a layout. Index damage is
// handled at generation time (it requires a different box tree), so it is
// reported separately by RebuildIndex.
func Plan(layout *Layout, kinds []DamageKind) (patches []DamagePlan, rebuildIndex bool, err error) {
	for _, kind := range kinds {
		switch kind {
		case DamageIndex:
			rebuildIndex = true
		case DamageSegment:
			tl := largestTrack(layout)
			if tl == nil || len(tl.Frames) < 3 {
				return nil, false, fmt.Errorf("fixture: no track large enough for segment damage")
			}
			mid := tl.Frames[len(tl.Frames)/2]
			// Corrupt the essence payload, not the frame header: the frame
			// boundary must stay discoverable so the recovery scanner can
			// report a determinable (and time-exact) damaged interval.
			// Losing the header is a separate, stronger failure tested by
			// truncation and header-wipe cases.
			payloadStart := mid.Offset + essence.HeaderSize
			if layout.Spec.Format == model.ContainerMXF {
				payloadStart = mid.Offset + 20 + essence.HeaderSize
			}
			payloadLen := mid.Size
			if layout.Spec.Format == model.ContainerMXF {
				payloadLen = mid.Size - 20
			}
			patches = append(patches, DamagePlan{
				Kind: DamageSegment, Track: tl.Spec.Logical,
				At: payloadStart, Len: payloadLen - essence.HeaderSize,
			})
		case DamageTruncate:
			last := uint64(0)
			for _, tl := range layout.Tracks {
				for _, f := range tl.Frames {
					if f.Offset+f.Size > last {
						last = f.Offset + f.Size
					}
				}
			}
			if last == 0 {
				return nil, false, fmt.Errorf("fixture: empty layout")
			}
			patches = append(patches, DamagePlan{Kind: DamageTruncate, At: last - 1})
		case DamageLength:
			tl := largestTrack(layout)
			if tl == nil || len(tl.Frames) == 0 {
				return nil, false, fmt.Errorf("fixture: no track to damage")
			}
			mid := tl.Frames[len(tl.Frames)/2]
			patches = append(patches, DamagePlan{Kind: DamageLength, Track: tl.Spec.Logical, At: mid.Offset})
		}
	}
	return patches, rebuildIndex, nil
}

func largestTrack(layout *Layout) *TrackLayout {
	var best *TrackLayout
	for _, tl := range layout.Tracks {
		if best == nil || tl.MediaSize > best.MediaSize {
			best = tl
		}
	}
	return best
}

// Apply mutates a copy of data according to the damage plan. It returns the
// damaged bytes and the injections that were actually made, in offset order.
func Apply(layout *Layout, data []byte, plan []DamagePlan) ([]byte, []Injection, error) {
	out := append([]byte(nil), data...)
	var injections []Injection
	for _, d := range plan {
		tl := findTrack(layout, d.Track)
		switch d.Kind {
		case DamageTruncate:
			if d.At == 0 || d.At > uint64(len(out)) {
				return nil, nil, fmt.Errorf("fixture: truncate offset %d out of range", d.At)
			}
			kept := d.At
			out = out[:kept]
			inj := Injection{Kind: DamageTruncate, Offset: kept, Length: uint64(len(data)) - kept}
			if tl == nil {
				tl = trackAt(layout, kept)
			}
			if tl != nil {
				fillRange(&inj, tl, kept)
			}
			injections = append(injections, inj)
		case DamageSegment:
			if tl == nil {
				return nil, nil, fmt.Errorf("fixture: segment damage for unknown track %q", d.Track)
			}
			if d.At+d.Len > uint64(len(out)) {
				return nil, nil, fmt.Errorf("fixture: segment damage out of range")
			}
			mark := byte(0xDE)
			if layout.Spec.Format == model.ContainerMXF {
				// Corrupt the frame header magic so the payload cannot be
				// trusted while the KLV boundary remains discoverable.
				mark = 0x00
			}
			for i := d.At; i < d.At+d.Len; i++ {
				out[i] = mark
			}
			inj := Injection{Kind: DamageSegment, Offset: d.At, Length: d.Len}
			fillRange(&inj, tl, d.At)
			inj.Timescale = tl.Spec.Timescale
			inj.HasRange = true
			injections = append(injections, inj)
		case DamageLength:
			if tl == nil {
				return nil, nil, fmt.Errorf("fixture: length damage for unknown track %q", d.Track)
			}
			if err := inflateLength(layout, out, tl, d.At); err != nil {
				return nil, nil, err
			}
			inj := Injection{Kind: DamageLength, Offset: d.At, Length: 1}
			fillRange(&inj, tl, d.At)
			inj.Timescale = tl.Spec.Timescale
			injections = append(injections, inj)
		default:
			return nil, nil, fmt.Errorf("fixture: damage %q needs regeneration", d.Kind)
		}
	}
	sort.Slice(injections, func(i, j int) bool { return injections[i].Offset < injections[j].Offset })
	return out, injections, nil
}

func findTrack(layout *Layout, logical string) *TrackLayout {
	for _, tl := range layout.Tracks {
		if tl.Spec.Logical == logical {
			return tl
		}
	}
	return nil
}

// trackAt returns the track owning the frame that contains offset.
func trackAt(layout *Layout, offset uint64) *TrackLayout {
	for _, tl := range layout.Tracks {
		for _, f := range tl.Frames {
			if offset >= f.Offset && offset < f.Offset+f.Size {
				return tl
			}
		}
	}
	return nil
}

// inflateLength rewrites the declared length at offset so it claims more
// bytes than the file holds. For MXF that means the KLV BER length; for MP4
// it means the enclosing box's 32-bit size field (offset must point at a box
// header then).
func inflateLength(layout *Layout, out []byte, tl *TrackLayout, offset uint64) error {
	if offset+20 > uint64(len(out)) {
		return fmt.Errorf("fixture: length damage out of range")
	}
	if layout.Spec.Format == model.ContainerMXF {
		// Claim 0x7FFFFF bytes, far beyond the real KLV value. The frame
		// payload itself stays where it is; only the declared boundary lies.
		out[offset+16] = 0x83
		out[offset+17] = 0x7F
		out[offset+18] = 0xFF
		out[offset+19] = 0xFF
		return nil
	}
	// MP4: point the damage at the first frame, then walk back to the mdat
	// header and inflate its 32-bit size.
	boxOff := int64(-1)
	for i := int64(0); i+8 <= int64(len(out)); i++ {
		if string(out[i+4:i+8]) == "mdat" {
			boxOff = i
			break
		}
	}
	if boxOff < 0 {
		return fmt.Errorf("fixture: mdat not found for length damage")
	}
	out[boxOff] = 0x7F
	out[boxOff+1] = 0xFF
	out[boxOff+2] = 0xFF
	out[boxOff+3] = 0xF0
	return nil
}

// fillRange converts an injected byte offset into the tick interval of the
// frame that contains it. If the offset is past every frame the injection is
// still recorded but no range is claimed.
func fillRange(inj *Injection, tl *TrackLayout, offset uint64) {
	inj.Track = tl.Spec.Logical
	inj.TrackID = tl.Spec.TrackID
	tb := model.Timebase{Timescale: tl.Spec.Timescale}
	for _, f := range tl.Frames {
		if offset >= f.Offset && offset < f.Offset+f.Size {
			inj.StartTick, inj.EndTick = f.StartTick, f.StartTick+f.Duration
			inj.StartNS = tb.TickToNanos(inj.StartTick)
			inj.EndNS = tb.TickToNanos(inj.EndTick)
			inj.HasRange = true
			return
		}
	}
	if len(tl.Frames) > 0 {
		last := tl.Frames[len(tl.Frames)-1]
		if offset >= last.Offset {
			inj.StartTick, inj.EndTick = last.StartTick, last.StartTick+last.Duration
			inj.StartNS = tb.TickToNanos(inj.StartTick)
			inj.EndNS = tb.TickToNanos(inj.EndTick)
			inj.HasRange = true
		}
	}
}
