// Package ingest reads CMAF/fMP4 input in a bounded, fault-tolerant way:
// malformed box lengths are recorded, truncated boxes are isolated, and the
// scanner resynchronises on the next valid moof so later fragments survive.
package ingest

import (
	"encoding/binary"
	"errors"
	"fmt"

	"mediaroute/internal/model"
)

// Box header sizes from ISO/IEC 14496-12.
const (
	SmallHeaderSize = 8
	LargeHeaderSize = 16
)

// Header/box parse failures. Callers classify them into model categories and
// attach the byte offset where the anomaly starts.
var (
	ErrIncompleteHeader = errors.New("incomplete box header")
	ErrInvalidSize      = errors.New("invalid box size")
	ErrSizeOverflow     = errors.New("box size overflow")
	ErrTruncatedPayload = errors.New("box payload exceeds available input")
)

// BoxError attaches the structural numbers that made a header unreadable, so
// issues can be recorded without reparsing error text.
type BoxError struct {
	Kind      error
	Type      string
	Declared  uint64
	Available uint64
}

func (e *BoxError) Error() string {
	return fmt.Sprintf("%v: type=%q declared=%d available=%d", e.Kind, e.Type, e.Declared, e.Available)
}

// Unwrap exposes the sentinel kind for errors.Is.
func (e *BoxError) Unwrap() error { return e.Kind }

// BoxHeader is a validated box header. Size includes the header.
type BoxHeader struct {
	Type        string
	Offset      uint64
	Size        uint64
	HeaderSize  uint64
	Declared    uint64
	ExtendToEOF bool
}

// PayloadOffset returns the absolute offset of the box payload.
func (h BoxHeader) PayloadOffset() uint64 { return h.Offset + h.HeaderSize }

// End returns the exclusive end offset of the box.
func (h BoxHeader) End() uint64 { return h.Offset + h.Size }

// ReadBoxHeaderAt parses a box header at off. available must equal the number
// of bytes between off and the end of the input.
func ReadBoxHeaderAt(data []byte, off, available uint64) (BoxHeader, error) {
	if off > uint64(len(data)) || available < SmallHeaderSize || uint64(len(data))-off < SmallHeaderSize {
		return BoxHeader{}, &BoxError{Kind: ErrIncompleteHeader, Available: available}
	}
	raw := data[off : off+SmallHeaderSize]
	size32 := binary.BigEndian.Uint32(raw[0:4])
	h := BoxHeader{
		Offset:     off,
		Type:       string(raw[4:8]),
		Declared:   uint64(size32),
		HeaderSize: SmallHeaderSize,
	}
	switch size32 {
	case 0:
		// The box extends to the end of the input; legal for a trailing mdat.
		h.ExtendToEOF = true
		h.Size = available
	case 1:
		if available < LargeHeaderSize || uint64(len(data))-off < LargeHeaderSize {
			return BoxHeader{}, &BoxError{Kind: ErrIncompleteHeader, Type: h.Type, Available: available}
		}
		h.HeaderSize = LargeHeaderSize
		h.Declared = binary.BigEndian.Uint64(data[off+8 : off+16])
		h.Size = h.Declared
	default:
		h.Size = uint64(size32)
	}
	if h.Size < h.HeaderSize {
		return BoxHeader{}, &BoxError{Kind: ErrInvalidSize, Type: h.Type, Declared: h.Declared, Available: available}
	}
	if h.Size > available {
		return BoxHeader{}, &BoxError{Kind: ErrTruncatedPayload, Type: h.Type, Declared: h.Size, Available: available}
	}
	return h, nil
}

// sizeSane reports whether a candidate header found while resynchronising is
// structurally plausible. Candidates are scanned inside damaged data, so the
// check does not require a known box type.
func sizeSane(data []byte, off uint64) bool {
	if uint64(len(data))-off < SmallHeaderSize {
		return false
	}
	size32 := binary.BigEndian.Uint32(data[off : off+4])
	if size32 == 0 {
		return false // "to EOF" is not a usable resync anchor
	}
	var size uint64
	if size32 == 1 {
		if uint64(len(data))-off < LargeHeaderSize {
			return false
		}
		size = binary.BigEndian.Uint64(data[off+8 : off+16])
	} else {
		size = uint64(size32)
	}
	if size < SmallHeaderSize || size > uint64(len(data))-off {
		return false
	}
	return printableType(data[off+4 : off+8])
}

func printableType(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// ResyncMoof scans forward from `from` for the next structurally valid moof
// header that parses completely within the input.
func ResyncMoof(data []byte, from, maxBoxSize uint64) (uint64, bool) {
	if from >= uint64(len(data)) {
		return 0, false
	}
	for pos := from; pos+SmallHeaderSize <= uint64(len(data)); pos++ {
		if string(data[pos+4:pos+8]) != "moof" {
			continue
		}
		if !sizeSane(data, pos) {
			continue
		}
		h, err := ReadBoxHeaderAt(data, pos, uint64(len(data))-pos)
		if err != nil || h.Type != "moof" {
			continue
		}
		if maxBoxSize > 0 && h.Size > maxBoxSize {
			continue
		}
		return pos, true
	}
	return 0, false
}

// TopBox is a top-level box retained with its payload bytes. Payload is a
// subslice of the scanned input, so scanning makes no copies.
type TopBox struct {
	Header  BoxHeader
	Payload []byte
}

// Damage is a damaged byte region isolated by Scan, in source order relative
// to the surrounding boxes.
type Damage struct {
	Offset       uint64
	Available    uint64
	DeclaredSize uint64
	Category     model.Category
	Detail       string
	RecoveredAt  uint64 // 0 when no boundary was recovered
	Recovered    bool
}

// Range returns the damaged byte interval.
func (d Damage) Range() model.ByteRange { return model.RangeOf(d.Offset, d.Available) }

// ScanItem is one ordered element of a structural scan: exactly one of Box or
// Damage is set.
type ScanItem struct {
	Box    *TopBox
	Damage *Damage
}

// ScanStats aggregates structural damage counts.
type ScanStats struct {
	Truncated     int
	Overflow      int
	Invalid       int
	TrailingBytes uint64
}

// maxIssues bounds audit memory so pathological input cannot exhaust it.
const maxIssues = 256

// Scan walks top-level boxes, isolating damaged regions as ordered items. A box
// whose declared length exceeds the remaining input yields a truncated_box
// damage item; the scanner then resynchronises on the next valid moof and
// continues. If no boundary can be recovered the leftover bytes are reported
// in TrailingBytes and callers must treat the input as unrecoverable.
func Scan(data []byte, maxBoxSize uint64) ([]ScanItem, ScanStats) {
	var stats ScanStats
	items := make([]ScanItem, 0, 16)
	var off uint64
	total := uint64(len(data))
	for off < total {
		available := total - off
		h, err := ReadBoxHeaderAt(data, off, available)
		if err == nil && maxBoxSize > 0 && h.Size > maxBoxSize {
			err = &BoxError{Kind: ErrSizeOverflow, Type: h.Type, Declared: h.Size, Available: available}
		}
		if err != nil {
			var be *BoxError
			errors.As(err, &be)
			dmg := classify(be, off, available, &stats)
			next, ok := ResyncMoof(data, off+1, maxBoxSize)
			if ok {
				dmg.Recovered = true
				dmg.RecoveredAt = next
			}
			items = append(items, ScanItem{Damage: &dmg})
			if !ok {
				stats.TrailingBytes = total - off
				break
			}
			off = next
			continue
		}
		box := TopBox{Header: h, Payload: data[h.PayloadOffset():h.End()]}
		items = append(items, ScanItem{Box: &box})
		off = h.End()
	}
	return items, stats
}

func classify(be *BoxError, off, available uint64, stats *ScanStats) Damage {
	switch {
	case errors.Is(be.Kind, ErrTruncatedPayload):
		stats.Truncated++
		return Damage{
			Offset: off, Available: available, DeclaredSize: be.Declared,
			Category: model.CategoryTruncatedBox,
			Detail:   "declared box length exceeds remaining input",
		}
	case errors.Is(be.Kind, ErrIncompleteHeader):
		stats.Truncated++
		return Damage{
			Offset: off, Available: available,
			Category: model.CategoryTruncatedBox,
			Detail:   "box header truncated",
		}
	case errors.Is(be.Kind, ErrSizeOverflow):
		stats.Overflow++
		return Damage{
			Offset: off, Available: available, DeclaredSize: be.Declared,
			Category: model.CategoryLengthOverflow,
			Detail:   "box length exceeds configured maximum",
		}
	default:
		stats.Invalid++
		return Damage{
			Offset: off, Available: available, DeclaredSize: be.Declared,
			Category: model.CategoryInvalidBox,
			Detail:   be.Error(),
		}
	}
}

// IndexIssues converts damages to auditable issues, capped at maxIssues.
func IndexIssues(items []ScanItem) []model.Issue {
	out := make([]model.Issue, 0, 8)
	for _, it := range items {
		if it.Damage == nil {
			continue
		}
		if len(out) >= maxIssues {
			break
		}
		out = append(out, model.Issue{
			Category:     it.Damage.Category,
			Stage:        "ingest/boxes",
			Offset:       it.Damage.Offset,
			DeclaredSize: it.Damage.DeclaredSize,
			Available:    it.Damage.Available,
			Detail:       it.Damage.Detail,
		})
	}
	return out
}
