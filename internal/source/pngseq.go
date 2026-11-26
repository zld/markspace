package source

import (
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"

	"mediarepair/internal/fsx"
	"mediarepair/internal/media"
)

// DecodeConfig reads only the image header.
func DecodeConfig(path string) (image.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Config{}, media.E(media.CodeDecode, "image.config_open", err).WithFile(path)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return image.Config{}, media.E(media.CodeFormat, "image.config_decode", err).WithFile(path)
	}
	return cfg, nil
}

// DecodeFrame opens a manifest-relative frame and decodes PNG or JPEG.
func DecodeFrame(root, rel string) (image.Image, string, error) {
	full, err := fsx.SafeJoin(root, rel)
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, "", media.E(media.CodeDecode, "frame.open", err).WithFile(rel)
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		return nil, "", media.E(media.CodeDecode, "frame.decode", err).WithFile(rel)
	}
	return img, format, nil
}

// HashAndDecode streams hash and decode from the same file handle where
// possible. It is used by cache/tests to prove byte identity decisions.
func HashAndDecode(root, rel string) (string, image.Image, error) {
	full, err := fsx.SafeJoin(root, rel)
	if err != nil {
		return "", nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return "", nil, media.E(media.CodeDecode, "frame.open", err).WithFile(rel)
	}
	defer f.Close()

	h := sha256.New()
	tee := io.TeeReader(f, h)
	img, _, err := image.Decode(tee)
	if err != nil {
		return "", nil, media.E(media.CodeDecode, "frame.decode", err).WithFile(rel)
	}
	// image.Decode may not consume trailing bytes; hash the remainder too.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return "", nil, media.E(media.CodeDecode, "frame.hash_read", err).WithFile(rel)
	}
	return hex.EncodeToString(h.Sum(nil)), img, nil
}

func init() {
	// Explicit registrations make supported formats clear even if blank
	// imports are removed by tooling.
	image.RegisterFormat("png", "\x89PNG\r\n\x1a\n", png.Decode, png.DecodeConfig)
	image.RegisterFormat("jpeg", "\xff\xd8\xff", jpeg.Decode, jpeg.DecodeConfig)
}
