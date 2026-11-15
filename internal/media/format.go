// Package media defines the deterministic on-disk frame containers used by
// the generated sample videos and the encoder test double. These are plain
// local formats, not real media codecs: no claim of production encoding
// quality is made anywhere in the pipeline.
package media

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// VidMagic heads a raw grayscale frame container.
	VidMagic = "TCVID001"
	// OutMagic heads an RLE-compressed encoder-output container.
	OutMagic = "TCOUT001"
)

var (
	// ErrTruncated means a container ended before its declared frames did.
	ErrTruncated = errors.New("media file truncated")
	// ErrBadFormat means header/magic validation failed.
	ErrBadFormat = errors.New("media file malformed")
)

// Video is an in-memory grayscale clip: each frame holds Width*Height bytes.
type Video struct {
	Width  int
	Height int
	FPS    int
	Frames [][]byte
}

func headerLine(k, v string) string { return k + "=" + v + "\n" }

// offsetReader tracks how many header bytes were consumed so payload and
// padding offsets stay exact regardless of buffering.
type offsetReader struct {
	data []byte
	pos  int
}

func (o *offsetReader) readLine() (string, error) {
	start := o.pos
	for o.pos < len(o.data) && o.data[o.pos] != '\n' {
		o.pos++
	}
	if o.pos >= len(o.data) {
		return "", io.EOF
	}
	line := string(o.data[start:o.pos])
	o.pos++ // consume newline
	return line, nil
}

func parseHeaderAt(raw []byte, magic string) (map[string]int, int, error) {
	o := &offsetReader{data: raw}
	got, err := o.readLine()
	if err != nil || got != magic {
		return nil, 0, fmt.Errorf("%w: bad magic %q", ErrBadFormat, got)
	}
	fields := []string{"width", "height", "fps", "frames"}
	hdr := make(map[string]int, len(fields))
	for _, k := range fields {
		line, err := o.readLine()
		if err != nil {
			return nil, 0, fmt.Errorf("%w: missing header %s: %v", ErrBadFormat, k, err)
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || parts[0] != k {
			return nil, 0, fmt.Errorf("%w: expected %s, got %q", ErrBadFormat, k, line)
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n <= 0 {
			return nil, 0, fmt.Errorf("%w: invalid %s=%q", ErrBadFormat, k, parts[1])
		}
		hdr[k] = n
	}
	return hdr, o.pos, nil
}

func parseHeader(r *bufio.Reader, magic string) (map[string]int, error) {
	got, err := r.ReadString('\n')
	if err != nil || strings.TrimSpace(got) != magic {
		return nil, fmt.Errorf("%w: bad magic %q", ErrBadFormat, got)
	}
	fields := []string{"width", "height", "fps", "frames"}
	hdr := make(map[string]int, len(fields))
	for _, k := range fields {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("%w: missing header %s: %v", ErrBadFormat, k, err)
		}
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 || parts[0] != k {
			return nil, fmt.Errorf("%w: expected %s, got %q", ErrBadFormat, k, line)
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%w: invalid %s=%q", ErrBadFormat, k, parts[1])
		}
		hdr[k] = n
	}
	return hdr, nil
}

// WriteVideo writes a raw frame container.
func WriteVideo(path string, v *Video) error {
	if v.Width <= 0 || v.Height <= 0 || v.FPS <= 0 || len(v.Frames) == 0 {
		return fmt.Errorf("%w: invalid video dimensions", ErrBadFormat)
	}
	plane := v.Width * v.Height
	var buf bytes.Buffer
	buf.WriteString(VidMagic + "\n")
	buf.WriteString(headerLine("width", strconv.Itoa(v.Width)))
	buf.WriteString(headerLine("height", strconv.Itoa(v.Height)))
	buf.WriteString(headerLine("fps", strconv.Itoa(v.FPS)))
	buf.WriteString(headerLine("frames", strconv.Itoa(len(v.Frames))))
	for i, frame := range v.Frames {
		if len(frame) != plane {
			return fmt.Errorf("%w: frame %d has %d bytes, want %d", ErrBadFormat, i, len(frame), plane)
		}
		buf.Write(frame)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ReadVideo loads a raw frame container, rejecting truncated payloads.
func ReadVideo(path string) (*Video, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	hdr, err := parseHeader(r, VidMagic)
	if err != nil {
		return nil, err
	}
	plane := hdr["width"] * hdr["height"]
	v := &Video{Width: hdr["width"], Height: hdr["height"], FPS: hdr["fps"], Frames: make([][]byte, 0, hdr["frames"])}
	for i := 0; i < hdr["frames"]; i++ {
		frame := make([]byte, plane)
		if _, err := io.ReadFull(r, frame); err != nil {
			return nil, fmt.Errorf("%w: frame %d: %v", ErrTruncated, i, err)
		}
		v.Frames = append(v.Frames, frame)
	}
	extra, err := r.ReadByte()
	if err == nil || extra != 0 {
		return nil, fmt.Errorf("%w: unexpected trailing bytes", ErrBadFormat)
	}
	return v, nil
}

// Encoded is the decoded view of an encoder-output container.
type Encoded struct {
	Width  int
	Height int
	FPS    int
	Quant  int
	Frames [][]byte
}

// rleEncode packs a pixel plane as (varint run length, value byte) pairs.
func rleEncode(plane []byte) []byte {
	var buf bytes.Buffer
	var uvar [binary.MaxVarintLen64]byte
	for i := 0; i < len(plane); {
		val := plane[i]
		run := 1
		for i+run < len(plane) && plane[i+run] == val && run < 65535 {
			run++
		}
		n := binary.PutUvarint(uvar[:], uint64(run))
		buf.Write(uvar[:n])
		buf.WriteByte(val)
		i += run
	}
	return buf.Bytes()
}

func rleDecode(data []byte, want int) ([]byte, error) {
	out := make([]byte, 0, want)
	for len(data) > 0 {
		run, n := binary.Uvarint(data)
		if n <= 0 || int(n)+1 > len(data) || run == 0 || int(run) > want-len(out) {
			return nil, fmt.Errorf("%w: bad RLE run", ErrBadFormat)
		}
		val := data[n]
		for k := uint64(0); k < run; k++ {
			out = append(out, val)
		}
		data = data[n+1:]
	}
	if len(out) != want {
		return nil, fmt.Errorf("%w: RLE payload length mismatch", ErrTruncated)
	}
	return out, nil
}

// WriteEncoded writes quantized frames with RLE compression, then pads the
// file deterministically so its total size tracks the requested byte budget.
// The pad length is recorded in the header and skipped by ReadEncoded.
func WriteEncoded(path string, e *Encoded, targetBytes int) error {
	if e.Width <= 0 || e.Height <= 0 || e.FPS <= 0 || e.Quant <= 0 || len(e.Frames) == 0 {
		return fmt.Errorf("%w: invalid encoded dimensions", ErrBadFormat)
	}
	plane := e.Width * e.Height
	var payload bytes.Buffer
	for i, f := range e.Frames {
		if len(f) != plane {
			return fmt.Errorf("%w: frame %d size mismatch", ErrBadFormat, i)
		}
		payload.Write(rleEncode(f))
	}
	var hdr bytes.Buffer
	hdr.WriteString(OutMagic + "\n")
	hdr.WriteString(headerLine("width", strconv.Itoa(e.Width)))
	hdr.WriteString(headerLine("height", strconv.Itoa(e.Height)))
	hdr.WriteString(headerLine("fps", strconv.Itoa(e.FPS)))
	hdr.WriteString(headerLine("frames", strconv.Itoa(len(e.Frames))))
	hdr.WriteString(headerLine("quant", strconv.Itoa(e.Quant)))
	hdr.WriteString(headerLine("data_bytes", strconv.Itoa(payload.Len())))
	// Iterate until the pad value's own digit count stops changing, so the
	// final header length is accounted for in the size target.
	pad := 0
	for {
		hdr.Reset()
		hdr.WriteString(OutMagic + "\n")
		hdr.WriteString(headerLine("width", strconv.Itoa(e.Width)))
		hdr.WriteString(headerLine("height", strconv.Itoa(e.Height)))
		hdr.WriteString(headerLine("fps", strconv.Itoa(e.FPS)))
		hdr.WriteString(headerLine("frames", strconv.Itoa(len(e.Frames))))
		hdr.WriteString(headerLine("quant", strconv.Itoa(e.Quant)))
		hdr.WriteString(headerLine("data_bytes", strconv.Itoa(payload.Len())))
		hdr.WriteString(headerLine("pad_bytes", strconv.Itoa(pad)))
		next := targetBytes - hdr.Len() - payload.Len()
		if next < 0 {
			next = 0 // Payload exceeds target; emit no padding and stay honest.
		}
		if next == pad {
			break
		}
		pad = next
	}
	out := make([]byte, 0, hdr.Len()+payload.Len()+pad)
	out = append(out, hdr.Bytes()...)
	out = append(out, payload.Bytes()...)
	out = append(out, bytes.Repeat([]byte{0xA5}, pad)...)
	return os.WriteFile(filepath.Clean(path), out, 0o644)
}

// ReadEncoded validates the container and returns the decoded frames.
func ReadEncoded(path string) (*Encoded, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	hdr, headerSize, err := parseHeaderAt(raw, OutMagic)
	if err != nil {
		return nil, err
	}
	o := &offsetReader{data: raw, pos: headerSize}
	quantLine, err := o.readLine()
	if err != nil {
		return nil, fmt.Errorf("%w: missing quant: %v", ErrBadFormat, err)
	}
	quant, err := kvInt(quantLine, "quant")
	if err != nil {
		return nil, err
	}
	dataLine, err := o.readLine()
	if err != nil {
		return nil, fmt.Errorf("%w: missing data_bytes: %v", ErrBadFormat, err)
	}
	dataBytes, err := kvInt(dataLine, "data_bytes")
	if err != nil {
		return nil, err
	}
	padLine, err := o.readLine()
	if err != nil {
		return nil, fmt.Errorf("%w: missing pad_bytes: %v", ErrBadFormat, err)
	}
	padBytes, err := kvInt(padLine, "pad_bytes")
	if err != nil {
		return nil, err
	}
	headerSize = o.pos
	if headerSize+dataBytes > len(raw) {
		return nil, fmt.Errorf("%w: payload extends past end of file", ErrTruncated)
	}
	payload := raw[headerSize : headerSize+dataBytes]
	if len(raw) != headerSize+dataBytes+padBytes {
		return nil, fmt.Errorf("%w: declared %d+%d bytes but file has %d",
			ErrTruncated, dataBytes, padBytes, len(raw)-headerSize)
	}
	plane := hdr["width"] * hdr["height"]
	flat, err := rleDecode(payload, plane*hdr["frames"])
	if err != nil {
		return nil, err
	}
	e := &Encoded{Width: hdr["width"], Height: hdr["height"], FPS: hdr["fps"], Quant: quant,
		Frames: make([][]byte, hdr["frames"])}
	for i := range e.Frames {
		e.Frames[i] = flat[i*plane : (i+1)*plane : (i+1)*plane]
	}
	return e, nil
}

func kvInt(line, key string) (int, error) {
	parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
	if len(parts) != 2 || parts[0] != key {
		return 0, fmt.Errorf("%w: expected %s, got %q", ErrBadFormat, key, line)
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: invalid %s=%q", ErrBadFormat, key, parts[1])
	}
	return n, nil
}

// Fingerprint returns the hex SHA-256 digest of a file's bytes.
func Fingerprint(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
