// Package index owns the durable, local processing state: input fingerprints,
// the parse index, chunk checkpoints and the metadata cache. Nothing here
// depends on how media is parsed; the caller supplies the byte regions that
// make up the container and the timed metadata so each can be invalidated
// independently.
package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Class says which digest a byte region contributes to.
type Class uint8

const (
	// ClassContainer is packaging: box headers and structural payloads
	// (ftyp, moov, moof, mdat headers, ...). A change here invalidates the
	// parse index because sample offsets and the box tree may have moved.
	ClassContainer Class = iota + 1
	// ClassMetadata is timed or descriptive metadata (udta, meta, emsg,
	// subtitle/chapter track payloads). A change here invalidates only the
	// metadata cache and the metadata records, not the sample index.
	ClassMetadata
	// ClassMedia is raw media payload. It contributes to the content digest
	// alone: a re-encode with an unchanged container does not move offsets.
	ClassMedia
)

// Region is a half-open byte range [Offset, Offset+Size) of an input file and
// the class of bytes it holds.
type Region struct {
	Offset int64
	Size   int64
	Class  Class
}

// End returns the exclusive end offset.
func (r Region) End() int64 { return r.Offset + r.Size }

// Fingerprint is the three-way identity of one input file. Content is the
// whole-file digest and is the primary identity used for resume and cache
// reuse; Container and Metadata are computed over disjoint regions so a
// change to one can be told apart from a change to the other.
type Fingerprint struct {
	Size      int64  `json:"size"`
	Content   string `json:"content_sha256"`
	Container string `json:"container_sha256"`
	Metadata  string `json:"metadata_sha256"`
}

// Valid reports whether all three digests are present.
func (f Fingerprint) Valid() bool {
	return f.Content != "" && f.Container != "" && f.Metadata != ""
}

// Equal reports exact identity.
func (f Fingerprint) Equal(o Fingerprint) bool {
	return f.Size == o.Size && f.Content == o.Content &&
		f.Container == o.Container && f.Metadata == o.Metadata
}

// Reuse describes what may be carried over when an input is seen again.
type Reuse struct {
	// ContentSame means the bytes are identical: everything may be reused.
	ContentSame bool
	// ContainerSame means packaging and offsets are unchanged. The metadata
	// cache and metadata records must still be discarded when MetadataSame is
	// false.
	ContainerSame bool
	// MetadataSame means timed metadata is unchanged.
	MetadataSame bool
}

// Plan decides reuse from a previous fingerprint.
func (p Fingerprint) Plan(previous Fingerprint) Reuse {
	return Reuse{
		ContentSame:   p.Valid() && previous.Valid() && p.Content == previous.Content,
		ContainerSame: p.Valid() && previous.Valid() && p.Container == previous.Container,
		MetadataSame:  p.Valid() && previous.Valid() && p.Metadata == previous.Metadata,
	}
}

// IndexReusable reports whether the parse index (track/box offsets) survives.
func (r Reuse) IndexReusable() bool { return r.ContentSame || r.ContainerSame }

// MetadataReusable reports whether cached metadata survives.
func (r Reuse) MetadataReusable() bool { return r.ContentSame || r.MetadataSame }

// FullReuse reports whether everything survives.
func (r Reuse) FullReuse() bool { return r.ContentSame }

// ComputeFingerprint hashes a file given its region layout. Regions must be
// sorted, non-overlapping and inside the file; bytes covered by no region are
// hashed into Content only. An uncovered gap is allowed (media payloads are
// normally left out of Container and Metadata), but a gap inside a region is
// not possible by construction.
func ComputeFingerprint(path string, regions []Region) (Fingerprint, error) {
	f, err := os.Open(path)
	if err != nil {
		return Fingerprint{}, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Fingerprint{}, err
	}
	if err := validateRegions(regions, info.Size()); err != nil {
		return Fingerprint{}, fmt.Errorf("%s: %w", path, err)
	}

	content := sha256.New()
	if _, err := io.Copy(content, f); err != nil {
		return Fingerprint{}, fmt.Errorf("hash %s: %w", path, err)
	}
	container, err := hashRegions(f, regions, ClassContainer)
	if err != nil {
		return Fingerprint{}, err
	}
	metadata, err := hashRegions(f, regions, ClassMetadata)
	if err != nil {
		return Fingerprint{}, err
	}
	return Fingerprint{
		Size:      info.Size(),
		Content:   hex.EncodeToString(content.Sum(nil)),
		Container: container,
		Metadata:  metadata,
	}, nil
}

func hashRegions(f *os.File, regions []Region, class Class) (string, error) {
	h := sha256.New()
	// Include the class and each region's extent so reclassifying a byte
	// changes the digest even if its value does not.
	var hdr [9]byte
	binary.BigEndian.PutUint64(hdr[:8], uint64(class))
	h.Write(hdr[:8])
	for _, r := range regions {
		if r.Class != class {
			continue
		}
		binary.BigEndian.PutUint64(hdr[:8], uint64(r.Offset))
		hdr[8] = ':'
		h.Write(hdr[:])
		binary.BigEndian.PutUint64(hdr[:8], uint64(r.Size))
		h.Write(hdr[:8])
		if _, err := io.Copy(h, io.NewSectionReader(f, r.Offset, r.Size)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validateRegions(regions []Region, size int64) error {
	prevEnd := int64(0)
	for i, r := range regions {
		if r.Offset < 0 || r.Size < 0 || r.End() > size {
			return fmt.Errorf("region %d [%d,%d) outside file of %d bytes", i, r.Offset, r.End(), size)
		}
		if r.Offset < prevEnd {
			return fmt.Errorf("regions overlap or are unsorted at region %d [%d,%d)", i, r.Offset, r.End())
		}
		if r.Class != ClassContainer && r.Class != ClassMetadata && r.Class != ClassMedia {
			return fmt.Errorf("region %d has unknown class %d", i, r.Class)
		}
		prevEnd = r.End()
	}
	return nil
}

// topLevelBox is the minimal header knowledge needed to classify a box.
type topLevelBox struct {
	offset int64
	size   int64
	typ    string
}

// metadataBoxes are ISO-BMFF boxes whose payload is timed or descriptive
// metadata. Their contents can change without moving any sample offsets.
var metadataBoxes = map[string]bool{
	"emsg": true,
	"udta": true,
	"meta": true,
	"chpl": true,
}

// mediaBoxes carry sample payloads and are excluded from Container.
var mediaBoxes = map[string]bool{
	"mdat": true,
}

// ScanBoxRegions walks the top-level box headers of a file and classifies each
// box by type. It is the conservative default used when the media parser has no
// finer track information: a moov change counts as a container change, which
// rebuilds the index. A truncated final box is an error: the file layout is not
// trustworthy and nothing may be reused.
func ScanBoxRegions(path string) ([]Region, []topLevelBox, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	size := info.Size()

	var (
		regions []Region
		boxes   []topLevelBox
	)
	for off := int64(0); off < size; {
		hdr := make([]byte, 8)
		if _, err := f.ReadAt(hdr, off); err != nil {
			return nil, nil, fmt.Errorf("%s: truncated box header at offset %d: %w", path, off, err)
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		headerLen := int64(8)
		switch boxSize {
		case 0: // extends to end of file
			boxSize = size - off
		case 1:
			ext := make([]byte, 8)
			if _, err := f.ReadAt(ext, off+8); err != nil {
				return nil, nil, fmt.Errorf("%s: truncated large box size at offset %d: %w", path, off, err)
			}
			boxSize = int64(binary.BigEndian.Uint64(ext))
			headerLen = 16
		}
		if boxSize < headerLen || off+boxSize > size {
			return nil, nil, fmt.Errorf("%s: box %q at offset %d declares size %d beyond file end %d",
				path, printableType(typ), off, boxSize, size)
		}
		boxes = append(boxes, topLevelBox{offset: off, size: boxSize, typ: typ})
		payload := Region{Offset: off + headerLen, Size: boxSize - headerLen}
		switch {
		case mediaBoxes[typ]:
			payload.Class = ClassMedia
		case metadataBoxes[typ]:
			payload.Class = ClassMetadata
		default:
			payload.Class = ClassContainer
		}
		if payload.Size > 0 {
			regions = append(regions, payload)
		}
		off += boxSize
	}
	return regions, boxes, nil
}

// Boxes returns the top-level box layout found by ScanBoxRegions.
func Boxes(path string) ([]topLevelBox, error) {
	_, boxes, err := ScanBoxRegions(path)
	return boxes, err
}

func printableType(t string) string {
	for _, c := range []byte(t) {
		if c < 0x20 || c > 0x7e {
			return "?"
		}
	}
	return t
}

// SortRegions orders and merges adjacent same-class regions. It is exported so
// the media parser can hand back a tidy layout.
func SortRegions(regions []Region) []Region {
	out := append([]Region(nil), regions...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		return out[i].Class < out[j].Class
	})
	return out
}

// ErrNoSuchInput is returned by lookup helpers when a path was never indexed.
var ErrNoSuchInput = errors.New("input not present in index")
