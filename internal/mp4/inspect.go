// Package mp4 inspects MP4 container metadata independently of the PCM
// content. Container changes (brands, timescale, duration or box layout)
// produce a different container digest while leaving PCM-derived assets
// untouched.
package mp4

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	go_mp4 "github.com/abema/go-mp4"

	"continuity/internal/media"
)

// Metadata is the reviewable container description extracted from a file.
type Metadata struct {
	Brand        string
	MinorVersion uint32
	Timescale    uint32
	Duration     uint64
	// BoxSignature is the ordered sequence of nested box types, which
	// changes whenever the encapsulation layout changes.
	BoxSignature []string
}

// Inspect parses the container structure of r. It never reads sample
// payloads, only box headers and the metadata boxes ftyp/mvhd/mdhd.
func Inspect(r io.ReadSeeker) (Metadata, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, media.WrapError(media.CatIO, "mp4: seek", err)
	}
	var (
		md       Metadata
		sigParts []string
	)
	_, err := go_mp4.ReadBoxStructure(r, func(h *go_mp4.ReadHandle) (interface{}, error) {
		typeName := h.BoxInfo.Type.String()
		sigParts = append(sigParts, strings.Repeat(".", len(h.Path))+typeName)
		switch h.BoxInfo.Type {
		case go_mp4.BoxTypeFtyp():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, media.WrapError(media.CatInvalidInput, "mp4: ftyp payload", perr)
			}
			ftyp, ok := box.(*go_mp4.Ftyp)
			if !ok {
				return nil, media.NewError(media.CatInvalidInput, "mp4: unexpected ftyp payload type")
			}
			md.Brand = string(ftyp.MajorBrand[:])
			md.MinorVersion = ftyp.MinorVersion
		case go_mp4.BoxTypeMvhd(), go_mp4.BoxTypeMdhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, media.WrapError(media.CatInvalidInput, "mp4: metadata payload", perr)
			}
			switch b := box.(type) {
			case *go_mp4.Mvhd:
				md.Timescale = b.Timescale
				md.Duration = b.GetDuration()
			case *go_mp4.Mdhd:
				// Prefer the first track media header only when the movie
				// header did not provide a timescale.
				if md.Timescale == 0 {
					md.Timescale = b.Timescale
				}
				if md.Duration == 0 {
					md.Duration = b.GetDuration()
				}
			}
		}
		return h.Expand()
	})
	if err != nil {
		if me, ok := err.(*media.Error); ok {
			return Metadata{}, me
		}
		return Metadata{}, media.WrapError(media.CatInvalidInput, "mp4: structure parse failed", err)
	}
	md.BoxSignature = sigParts
	return md, nil
}

// Digest computes the container digest from metadata only, so two files
// with identical PCM but different encapsulation hash differently.
func Digest(md Metadata) string {
	h := sha256.New()
	h.Write([]byte("continuity-container-v1\n"))
	writeLine := func(s string) { h.Write([]byte(s)); h.Write([]byte{'\n'}) }
	writeLine(md.Brand)
	writeLine(itoa32(md.MinorVersion))
	writeLine(itoa32(md.Timescale))
	writeLine(itoa64(md.Duration))
	writeLine(strings.Join(md.BoxSignature, ","))
	return hex.EncodeToString(h.Sum(nil))
}

func itoa32(v uint32) string { return itoa64(uint64(v)) }

func itoa64(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
