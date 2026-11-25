// Package mp4 parses the MP4-compatible box envelope and synthetic mcmf
// manifest. Standard box headers are read with github.com/abema/go-mp4.
package mp4

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	gomp4 "github.com/abema/go-mp4"

	"github.com/zld/media-check/internal/fault"
)

type Box struct {
	Type          string `json:"type"`
	Offset        int64  `json:"offset"`
	Size          int64  `json:"size"`
	HeaderSize    int64  `json:"header_size"`
	PayloadOffset int64  `json:"payload_offset"`
	PayloadSize   int64  `json:"payload_size"`
	DeclaredEnd   int64  `json:"declared_end"`
}

type Container struct {
	FileSize int64     `json:"file_size"`
	Boxes    []*Box    `json:"boxes"`
	Manifest *Manifest `json:"manifest,omitempty"`
	Mdat     *Box      `json:"mdat,omitempty"`
}

type ParseResult struct {
	Container *Container
	Issues    []*fault.Error
}

func BuildBox(boxType string, payload []byte) ([]byte, error) {
	if len(boxType) != 4 {
		return nil, fmt.Errorf("box type must be four characters: %q", boxType)
	}
	size := int64(8 + len(payload))
	if size > math.MaxUint32 {
		return nil, fmt.Errorf("box payload too large: %d", len(payload))
	}
	out := make([]byte, 0, size)
	var header [8]byte
	header[0] = byte(size >> 24)
	header[1] = byte(size >> 16)
	header[2] = byte(size >> 8)
	header[3] = byte(size)
	copy(header[4:8], boxType)
	out = append(out, header[:]...)
	out = append(out, payload...)
	return out, nil
}

func BuildFileTypeBox() ([]byte, error) {
	payload := make([]byte, 0, 16)
	payload = append(payload, 'i', 's', 'o', 'm')
	payload = append(payload, 0, 0, 2, 0) // minor version 512
	payload = append(payload, 'i', 's', 'o', 'm')
	payload = append(payload, 'm', 'p', '4', '2')
	return BuildBox(BoxFileType, payload)
}

// BuildFile serializes ftyp, mcmf and mdat. Sample.MdatOffset values are
// relative to the mdat payload and therefore remain stable here.
func BuildFile(manifest *Manifest, mdatPayload []byte) ([]byte, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest is required")
	}
	manifest.Version = ManifestVersion
	if manifest.TimelineScale == 0 {
		manifest.TimelineScale = TimelineScale
	}
	mf, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	ftyp, err := BuildFileTypeBox()
	if err != nil {
		return nil, err
	}
	mcmf, err := BuildBox(BoxManifest, mf)
	if err != nil {
		return nil, err
	}
	mdat, err := BuildBox(BoxMediaData, mdatPayload)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(ftyp)+len(mcmf)+len(mdat))
	out = append(out, ftyp...)
	out = append(out, mcmf...)
	out = append(out, mdat...)
	return out, nil
}

func Parse(data []byte, inputID, inputSHA string) *ParseResult {
	res := &ParseResult{Container: &Container{FileSize: int64(len(data))}}
	reader := bytes.NewReader(data)
	var offset int64

	for offset < int64(len(data)) {
		if _, err := reader.Seek(offset, 0); err != nil {
			res.Issues = append(res.Issues, fault.E(fault.StageMP4, fault.CodeReadFailed,
				"cannot seek to box header", fault.WithPath(inputID),
				fault.WithInput(inputID, inputSHA), fault.WithOffset(offset, 0), fault.WithCause(err)))
			return res
		}
		bi, err := gomp4.ReadBoxInfo(reader)
		if err != nil {
			firstBad := int64(len(data))
			res.Issues = append(res.Issues, fault.E(fault.StageMP4, fault.CodeTruncatedBox,
				"truncated box header", fault.WithPath(inputID),
				fault.WithInput(inputID, inputSHA), fault.WithOffset(firstBad, 0),
				fault.WithBoxOffset(offset), fault.WithCause(err)))
			return res
		}

		start := int64(bi.Offset)
		headerSize := int64(bi.HeaderSize)
		size := safeU64ToI64(bi.Size)
		end, overflow := add64(start, size)
		if overflow || size < headerSize {
			res.Issues = append(res.Issues, fault.E(fault.StageMP4, fault.CodeInvalidBox,
				fmt.Sprintf("invalid box size=%d header=%d", bi.Size, bi.HeaderSize),
				fault.WithPath(inputID), fault.WithInput(inputID, inputSHA),
				fault.WithBoxOffset(start), fault.WithOffset(start, size)))
			return res
		}

		box := &Box{
			Type:          string(bi.Type[:]),
			Offset:        start,
			Size:          size,
			HeaderSize:    headerSize,
			PayloadOffset: start + headerSize,
			PayloadSize:   size - headerSize,
			DeclaredEnd:   end,
		}

		if end > int64(len(data)) {
			missing := end - int64(len(data))
			res.Issues = append(res.Issues, fault.E(fault.StageMP4, fault.CodeTruncatedBox,
				fmt.Sprintf("box %q declares end %d but input ends at %d", box.Type, end, len(data)),
				fault.WithPath(inputID), fault.WithInput(inputID, inputSHA),
				fault.WithOffset(int64(len(data)), missing), fault.WithBoxOffset(start)))
			res.Container.Boxes = append(res.Container.Boxes, box)
			if box.Type == BoxMediaData {
				res.Container.Mdat = box
			}
			return res
		}

		res.Container.Boxes = append(res.Container.Boxes, box)
		if box.Type == BoxMediaData {
			res.Container.Mdat = box
		}
		if box.Type == BoxManifest {
			payload := data[box.PayloadOffset:end]
			var m Manifest
			if err := json.Unmarshal(payload, &m); err != nil {
				res.Issues = append(res.Issues, fault.E(fault.StageMP4, fault.CodeInvalidBox,
					"cannot decode mcmf manifest", fault.WithPath(inputID),
					fault.WithInput(inputID, inputSHA), fault.WithOffset(box.PayloadOffset, box.PayloadSize),
					fault.WithBoxOffset(start), fault.WithCause(err)))
				return res
			}
			res.Container.Manifest = &m
		}
		offset = end
	}

	if res.Container.Manifest == nil {
		res.Issues = append(res.Issues, fault.E(fault.StageMP4, fault.CodeInvalidBox,
			"missing mcmf manifest box", fault.WithPath(inputID), fault.WithInput(inputID, inputSHA)))
	}
	if res.Container.Mdat == nil {
		res.Issues = append(res.Issues, fault.E(fault.StageMP4, fault.CodeInvalidBox,
			"missing mdat media box", fault.WithPath(inputID), fault.WithInput(inputID, inputSHA)))
	}
	return res
}

func safeU64ToI64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

func add64(a, b int64) (int64, bool) {
	if b < 0 || a > math.MaxInt64-b {
		return 0, true
	}
	return a + b, false
}
