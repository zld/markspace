package container

import (
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediarc/model"
)

// recoveredTrackID is the synthetic track identifier for boundary-recovered
// samples that the index could not attribute.
const recoveredTrackID = 0

func toModel(err error) *model.Error {
	if m, ok := err.(*model.Error); ok {
		return m
	}
	return model.New(model.CodeCorruptBox, stream, err.Error())
}

// mdatRegion is an mdat box body that is fully inside the file.
type mdatRegion struct {
	// Start is the first payload byte and End the exclusive end.
	Start int64
	End   int64
}

// scanTopLevel walks the top-level box chain. On a damaged header it records
// the failure offset and then resynchronises: it searches forward for the next
// plausible header and continues, so later valid fragments are recovered
// without reading past the end of the input.
func scanTopLevel(r io.ReadSeeker, size int64) ([]mp4.BoxInfo, []mdatRegion, error) {
	boxes := make([]mp4.BoxInfo, 0, 16)
	mdats := make([]mdatRegion, 0, 4)
	var firstErr error
	offset := int64(0)
	for offset+mp4.SmallHeaderSize <= size {
		if _, err := r.Seek(offset, io.SeekStart); err != nil {
			return boxes, mdats, model.New(model.CodeTruncatedInput, stream, "seek failed").
				WithOffset(offset).WithCause(err)
		}
		info, err := mp4.ReadBoxInfo(r)
		if err != nil {
			if firstErr == nil {
				firstErr = model.New(model.CodeTruncatedInput, stream,
					"cannot read box header").WithOffset(offset).WithCause(err)
			}
			next := resync(r, offset+1, size)
			if next < 0 {
				return boxes, mdats, firstErr
			}
			offset = next
			continue
		}
		end := int64(info.Offset + info.Size)
		if info.Size < info.HeaderSize || end > size {
			if firstErr == nil {
				firstErr = model.New(model.CodeTruncatedInput, stream,
					fmt.Sprintf("box %s declares %d bytes but only %d remain",
						info.Type, info.Size, size-int64(info.Offset))).WithOffset(int64(info.Offset))
			}
			if !knownBoxTypes[info.Type.String()] {
				// An implausible header inside a damaged region: resynchronise
				// onto the next real box instead of trusting the size.
				if next := resync(r, offset+1, size); next >= 0 {
					offset = next
					continue
				}
				return boxes, mdats, firstErr
			}
			// The box header is readable but its payload is cut off. Its
			// readable prefix is still a valid boundary region, then scanning
			// stops: nothing after a truncated payload can be located safely.
			if info.Type == mp4.BoxTypeMdat() && end > size {
				bodyStart := int64(info.Offset) + int64(info.HeaderSize)
				if bodyStart < size {
					mdats = append(mdats, mdatRegion{Start: bodyStart, End: size})
				}
			}
			return boxes, mdats, firstErr
		}
		if info.Size < mp4.SmallHeaderSize {
			// Zero-size boxes that are not "extends to EOF" are meaningless.
			if firstErr == nil {
				firstErr = model.New(model.CodeCorruptBox, stream,
					fmt.Sprintf("box %s has an impossible size", info.Type)).WithOffset(int64(info.Offset))
			}
			next := resync(r, offset+1, size)
			if next < 0 {
				return boxes, mdats, firstErr
			}
			offset = next
			continue
		}
		boxes = append(boxes, *info)
		if info.Type == mp4.BoxTypeMdat() {
			mdats = append(mdats, mdatRegion{
				Start: int64(info.Offset) + int64(info.HeaderSize),
				End:   end,
			})
		}
		offset = end
	}
	if offset < size && firstErr == nil && size-offset > 0 {
		// Trailing bytes too short to hold a header are not recoverable.
		firstErr = model.New(model.CodeTruncatedInput, stream,
			fmt.Sprintf("%d trailing bytes are shorter than a box header", size-offset)).WithOffset(offset)
	}
	return boxes, mdats, firstErr
}

// knownBoxTypes are the fourccs a resynchronising scan accepts as a boundary.
var knownBoxTypes = map[string]bool{
	"ftyp": true, "moov": true, "mdat": true, "free": true, "skip": true,
	"moof": true, "mfra": true, "styp": true, "sidx": true, "wide": true,
	"uuid": true, "meta": true,
}

// resync searches for the next plausible top-level box header at or after from.
// It returns -1 when none exists. It never returns a position from which a box
// could run past size.
func resync(r io.ReadSeeker, from, size int64) int64 {
	header := make([]byte, mp4.SmallHeaderSize)
	for pos := from; pos+mp4.SmallHeaderSize <= size; pos++ {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return -1
		}
		if _, err := io.ReadFull(r, header); err != nil {
			return -1
		}
		boxType := string(header[4:8])
		if !knownBoxTypes[boxType] {
			continue
		}
		declared := int64(uint32(header[0])<<24 | uint32(header[1])<<16 | uint32(header[2])<<8 | uint32(header[3]))
		if declared < mp4.SmallHeaderSize || pos+declared > size {
			continue
		}
		return pos
	}
	return -1
}
