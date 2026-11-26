package source

import (
	"encoding/binary"
	"encoding/json"
	"fmt"

	"mediaprep/internal/model"
)

type scannedField struct {
	RawField
}

type scanResult struct {
	fields             []RawField
	codecByTrack       map[string]string
	codecOffsetByTrack map[string]int64
}

var containerBoxes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "stsd": true, "edts": true, "udta": true,
	"dinf": true, "mvex": true, "moof": true, "traf": true,
}

type boxHeader struct {
	typ          string
	start        int64
	headerSize   int64
	payloadStart int64
	end          int64
}

func readBoxHeader(data []byte, off, parentEnd int64, track string) (boxHeader, error) {
	if off+8 > parentEnd || off+8 > int64(len(data)) {
		return boxHeader{}, model.NewError(model.CodeTruncated, model.PhaseParse, track, off,
			"truncated MP4 box header")
	}
	size32 := binary.BigEndian.Uint32(data[off : off+4])
	typ := string(data[off+4 : off+8])
	h := boxHeader{typ: typ, start: off, headerSize: 8, payloadStart: off + 8}
	size := int64(size32)
	if size32 == 1 {
		if off+16 > parentEnd {
			return boxHeader{}, model.NewError(model.CodeTruncated, model.PhaseParse, track, off,
				"truncated 64-bit MP4 box length")
		}
		size = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
		h.headerSize = 16
		h.payloadStart = off + 16
	} else if size32 == 0 {
		size = parentEnd - off
	}
	if size < h.headerSize {
		return boxHeader{}, model.NewError(model.CodeLengthOutOfRange, model.PhaseParse, track, off,
			"MP4 box %q declared size %d is shorter than header %d", typ, size, h.headerSize)
	}
	h.end = off + size
	if h.end > parentEnd || h.end > int64(len(data)) || h.end < off {
		return boxHeader{}, model.NewError(model.CodeLengthOutOfRange, model.PhaseParse, track, off,
			"MP4 box %q length %d extends past source/parent end %d", typ, size, parentEnd)
	}
	return h, nil
}

// scanBoxes validates every box length before go-mp4 builds the sample table.
// The first invalid length is returned with its absolute source offset.
func scanBoxes(data []byte) (*scanResult, error) {
	res := &scanResult{
		codecByTrack:       map[string]string{},
		codecOffsetByTrack: map[string]int64{},
	}
	err := scanRange(data, 0, int64(len(data)), nil, "", res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func scanRange(data []byte, start, end int64, path []string, track string, res *scanResult) error {
	off := start
	for off < end {
		h, err := readBoxHeader(data, off, end, track)
		if err != nil {
			return err
		}
		currentTrack := track
		if h.typ == "tkhd" {
			if id, ok := readTKHDTrackID(data, h); ok {
				currentTrack = fmt.Sprintf("mp4:%d", id)
				track = currentTrack // tkhd is normally the first trak child
			}
		}
		if len(path) > 0 && path[len(path)-1] == "stsd" && currentTrack != "" {
			res.codecByTrack[currentTrack] = h.typ
			res.codecOffsetByTrack[currentTrack] = h.start
		}
		if h.typ == "mprv" {
			if err := collectProvenance(data, h, path, currentTrack, res); err != nil {
				return err
			}
		}
		if containerBoxes[h.typ] {
			childPath := append(append([]string{}, path...), h.typ)
			if err := scanRange(data, h.payloadStart, h.end, childPath, currentTrack, res); err != nil {
				return err
			}
		}
		off = h.end
	}
	return nil
}

func readTKHDTrackID(data []byte, h boxHeader) (uint32, bool) {
	// FullBox: version/flags at payload start. TrackID offset depends version.
	p := h.payloadStart
	if p+4 > int64(len(data)) {
		return 0, false
	}
	version := data[p]
	idOff := p + 12
	if version == 1 {
		idOff = p + 20
	}
	if idOff+4 > h.end {
		return 0, false
	}
	return binary.BigEndian.Uint32(data[idOff : idOff+4]), true
}

func collectProvenance(data []byte, h boxHeader, path []string, track string, res *scanResult) error {
	payload := data[h.payloadStart:h.end]
	var values map[string]string
	if err := json.Unmarshal(payload, &values); err != nil {
		return model.NewError(model.CodeInvalidMetadata, model.PhaseParse, track, h.start,
			"invalid mprv provenance payload: %v", err)
	}
	scope := ScopeContainer
	for _, p := range path {
		if p == "stsd" {
			scope = ScopeSampleOrDescriptor
			break
		}
		if p == "trak" {
			scope = ScopeTrack
		}
	}
	for name, value := range values {
		res.fields = append(res.fields, RawField{
			Name: name, Value: value, Scope: scope, Track: track, Offset: h.start,
		})
	}
	return nil
}
