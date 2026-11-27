package index

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediasync/internal/model"
)

// boxNode is a lightweight recursive view of one ISO-BMFF box.
type boxNode struct {
	info     mp4.BoxInfo
	payload  mp4.IBox // nil for container boxes or unsupported types
	children []*boxNode
}

// Scan opens path, hashes it, walks its box tree and normalizes every track
// into nanosecond sample boundaries. Recoverable per-track problems are
// collected in ScanResult.Anomalies; structural truncation returns an error.
func Scan(path string, limits model.Limits) (*ScanResult, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewError(model.ClassCorruptBox, "open %s: %v", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, model.NewError(model.ClassCorruptBox, "stat %s: %v", path, err)
	}
	if fi.Size() > limits.MaxFileBytes {
		return nil, model.AtError(model.ClassLimitExceeded, path, 0, 0,
			"file size %d exceeds limit %d", fi.Size(), limits.MaxFileBytes)
	}

	sum, err := hashFile(f)
	if err != nil {
		return nil, err
	}
	root, walkErr := walkBoxes(f, path, fi.Size())
	res := &ScanResult{
		InputPath:   path,
		InputSHA256: sum,
		InputSize:   fi.Size(),
	}
	if walkErr != nil {
		// Keep the hash so the failure remains attributable, then fail hard.
		return res, walkErr
	}

	if err := normalize(res, root, limits); err != nil {
		return res, err
	}
	return res, nil
}

func hashFile(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", model.NewError(model.ClassCorruptBox, "hashing: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// walkBoxes recursively parses box headers and payloads until EOF.
func walkBoxes(r io.ReadSeeker, path string, fileSize int64) ([]*boxNode, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var nodes []*boxNode
	for {
		start, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		if start >= fileSize {
			return nodes, nil
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, model.AtError(model.ClassTruncated, path, 0, start,
				"box header truncated at end of file")
		}
		if err != nil {
			return nil, model.AtError(model.ClassCorruptBox, path, 0, start,
				"invalid box header: %v", err)
		}
		if int64(bi.Offset+bi.Size) > fileSize {
			return nil, model.AtError(model.ClassTruncated, path, 0, int64(bi.Offset),
				"box %s declares size %d beyond file length %d", bi.Type, bi.Size, fileSize)
		}
		node := &boxNode{info: *bi}
		if isContainer(bi.Type) {
			payloadStart := int64(bi.Offset + bi.HeaderSize)
			if _, err := r.Seek(payloadStart, io.SeekStart); err != nil {
				return nil, err
			}
			childEnd := int64(bi.Offset + bi.Size)
			kids, err := walkUntil(r, path, childEnd)
			if err != nil {
				return nil, err
			}
			node.children = kids
		} else if bi.IsSupportedType() {
			box, perr := readPayload(r, bi)
			if perr != nil {
				return nil, model.AtError(model.ClassCorruptBox, path, 0, int64(bi.Offset),
					"cannot parse %s: %v", bi.Type, perr)
			}
			node.payload = box
		}
		nodes = append(nodes, node)
		if _, err := r.Seek(int64(bi.Offset+bi.Size), io.SeekStart); err != nil {
			return nil, err
		}
	}
}

func walkUntil(r io.ReadSeeker, path string, end int64) ([]*boxNode, error) {
	var nodes []*boxNode
	for {
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		if pos >= end {
			if pos != end {
				return nil, model.AtError(model.ClassCorruptBox, path, 0, pos,
					"child box overflows parent at %d", end)
			}
			return nodes, nil
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return nil, model.AtError(model.ClassTruncated, path, 0, pos,
				"child box header truncated: %v", err)
		}
		if int64(bi.Offset+bi.Size) > end {
			return nil, model.AtError(model.ClassTruncated, path, 0, int64(bi.Offset),
				"child %s size %d overflows parent ending %d", bi.Type, bi.Size, end)
		}
		node := &boxNode{info: *bi}
		if isContainer(bi.Type) {
			if _, err := r.Seek(int64(bi.Offset+bi.HeaderSize), io.SeekStart); err != nil {
				return nil, err
			}
			kids, err := walkUntil(r, path, int64(bi.Offset+bi.Size))
			if err != nil {
				return nil, err
			}
			node.children = kids
		} else if bi.IsSupportedType() {
			box, perr := readPayload(r, bi)
			if perr != nil {
				return nil, model.AtError(model.ClassCorruptBox, path, 0, int64(bi.Offset),
					"cannot parse %s: %v", bi.Type, perr)
			}
			node.payload = box
		}
		nodes = append(nodes, node)
		if _, err := r.Seek(int64(bi.Offset+bi.Size), io.SeekStart); err != nil {
			return nil, err
		}
	}
}

func isContainer(t mp4.BoxType) bool {
	switch t.String() {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts", "udta", "meta":
		return true
	default:
		return false
	}
}

// readPayload seeks to the box payload and unmarshals it into the concrete
// type registered for the box.
func readPayload(r io.ReadSeeker, bi *mp4.BoxInfo) (mp4.IBox, error) {
	if _, err := bi.SeekToPayload(r); err != nil {
		return nil, err
	}
	box, _, err := mp4.UnmarshalAny(r, bi.Type, bi.Size-bi.HeaderSize, bi.Context)
	return box, err
}
