// Package media handles Y4M (YUV4MPEG2) validation, streaming frame access,
// deterministic sample generation and segment splitting.
// Only 8-bit YUV420p is supported; frames are processed as streams so that
// memory use stays bounded regardless of clip length.
package media

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Header describes a validated Y4M stream.
type Header struct {
	Width   int
	Height  int
	FPSNum  int
	FPSDen  int
	Color   string
	FrameSize int // raw bytes of one YUV420p frame
}

// DurationSeconds returns the duration of n frames in seconds.
func (h Header) DurationSeconds(n int) float64 {
	return float64(n) * float64(h.FPSDen) / float64(h.FPSNum)
}

// Reader streams raw YUV420p frames from a Y4M input.
type Reader struct {
	br    *bufio.Reader
	hdr   Header
	frame int
}

// ErrTruncated marks a stream that ends in the middle of a frame.
var ErrTruncated = errors.New("truncated y4m frame")

// NewReader parses and validates the Y4M header. It rejects unsupported
// chroma formats, illegal dimensions and missing rate information.
func NewReader(r io.Reader) (*Reader, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("y4m: cannot read header: %w", err)
	}
	fields := strings.Fields(strings.TrimRight(line, "\r\n"))
	if len(fields) == 0 || fields[0] != "YUV4MPEG2" {
		return nil, errors.New("y4m: bad magic, not a YUV4MPEG2 stream")
	}
	h := Header{FPSNum: 25, FPSDen: 1, Color: "C420"} // Y4M defaults
	for _, f := range fields[1:] {
		if f == "" {
			continue
		}
		switch f[0] {
		case 'W':
			h.Width, _ = strconv.Atoi(f[1:])
		case 'H':
			h.Height, _ = strconv.Atoi(f[1:])
		case 'F':
			if n, d, ok := strings.Cut(f[1:], ":"); ok {
				h.FPSNum, _ = strconv.Atoi(n)
				h.FPSDen, _ = strconv.Atoi(d)
			}
		case 'C':
			h.Color = f
		}
	}
	if h.Width <= 0 || h.Height <= 0 {
		return nil, errors.New("y4m: illegal dimensions")
	}
	if h.Width%2 != 0 || h.Height%2 != 0 {
		return nil, fmt.Errorf("y4m: dimensions must be even for 4:2:0, got %dx%d", h.Width, h.Height)
	}
	if h.FPSNum <= 0 || h.FPSDen <= 0 {
		return nil, fmt.Errorf("y4m: illegal frame rate %d/%d", h.FPSNum, h.FPSDen)
	}
	// Only 8-bit 4:2:0 is accepted. C420jpeg/mpeg2/paldv are the same
	// planar 8-bit 4:2:0 layout with different chroma siting tags (FFmpeg's
	// decoder emits C420mpeg2); normalize them. C422/C444 and 10/12-bit
	// variants are rejected.
	switch h.Color {
	case "C420", "C420jpeg", "C420mpeg2", "C420paldv":
		h.Color = "C420"
	default:
		return nil, fmt.Errorf("y4m: unsupported chroma format %q, only 8-bit C420", h.Color)
	}
	h.FrameSize = h.Width * h.Height * 3 / 2
	return &Reader{br: br, hdr: h}, nil
}

// Header returns the validated stream header.
func (r *Reader) Header() Header { return r.hdr }

// FrameCount returns the number of frames read so far.
func (r *Reader) FrameCount() int { return r.frame }

// NextFrame reads one raw frame into buf (which must have FrameSize capacity).
// It returns io.EOF at a clean end of stream and ErrTruncated when the final
// frame is incomplete.
func (r *Reader) NextFrame(buf []byte) error {
	line, err := r.br.ReadString('\n')
	if errors.Is(err, io.EOF) && line == "" {
		return io.EOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("y4m: frame marker: %w", err)
	}
	if !strings.HasPrefix(strings.TrimRight(line, "\r\n"), "FRAME") {
		return fmt.Errorf("y4m: expected FRAME marker at frame %d, got %q", r.frame, line)
	}
	if _, err := io.ReadFull(r.br, buf[:r.hdr.FrameSize]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return fmt.Errorf("%w %d: %w", ErrTruncated, r.frame, err)
		}
		return err
	}
	r.frame++
	return nil
}

// WriteHeader emits a Y4M header line for the given stream parameters.
func WriteHeader(w io.Writer, h Header) error {
	_, err := fmt.Fprintf(w, "YUV4MPEG2 W%d H%d F%d:%d Ip A1:1 C420\n",
		h.Width, h.Height, h.FPSNum, h.FPSDen)
	return err
}

// SegmentRef points at a Y4M file holding one scene segment.
type SegmentRef struct {
	Index            int
	Path             string
	Frames           int
	Width, Height    int
	FPSNum, FPSDen   int
}

// Header rebuilds a Header for the segment file.
func (s SegmentRef) Header() Header {
	return Header{Width: s.Width, Height: s.Height, FPSNum: s.FPSNum, FPSDen: s.FPSDen,
		Color: "C420", FrameSize: s.Width * s.Height * 3 / 2}
}

// SplitToSegments streams r into per-segment Y4M files inside dir, each with at
// most framesPer frames. The final segment keeps its actual (shorter) length.
// A reusable frame buffer keeps memory bounded.
func SplitToSegments(r *Reader, dir string, framesPer int) ([]SegmentRef, error) {
	if framesPer <= 0 {
		return nil, errors.New("y4m: frames-per-segment must be positive")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	h := r.Header()
	buf := make([]byte, h.FrameSize)
	var refs []SegmentRef
	var out *os.File
	used := 0
	closeOut := func() error {
		if out == nil {
			return nil
		}
		err := out.Close()
		out = nil
		return err
	}
	defer closeOut()
	for {
		err := r.NextFrame(buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if out == nil || used == framesPer {
			if err := closeOut(); err != nil {
				return nil, err
			}
			idx := len(refs)
			p := filepath.Join(dir, fmt.Sprintf("segment_%03d.y4m", idx))
			f, err := os.Create(p)
			if err != nil {
				return nil, err
			}
			if err := WriteHeader(f, h); err != nil {
				f.Close()
				return nil, err
			}
			out = f
			refs = append(refs, SegmentRef{Path: p, Index: idx, Width: h.Width, Height: h.Height,
				FPSNum: h.FPSNum, FPSDen: h.FPSDen})
			used = 0
		}
		if _, err := out.WriteString("FRAME\n"); err != nil {
			return nil, err
		}
		if _, err := out.Write(buf); err != nil {
			return nil, err
		}
		used++
		refs[len(refs)-1].Frames++
	}
	if err := closeOut(); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, errors.New("y4m: input contains no frames")
	}
	return refs, nil
}

// DemoConfig controls deterministic sample generation.
type DemoConfig struct {
	Path               string
	Width, Height      int
	FPSNum, FPSDen     int
	Segments           int
	FramesPerSegment   int
}

// lcg is a tiny deterministic PRNG (Numerical Recipes constants).
type lcg struct{ state uint32 }

func (l *lcg) next() uint32 {
	l.state = l.state*1103515245 + 12345
	return l.state
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// GenerateDemo writes a deterministic multi-segment YUV420p Y4M file.
// Segment index drives noise amplitude and motion speed so that successive
// segments differ in complexity (and therefore in achievable bitrate).
func GenerateDemo(cfg DemoConfig) (int, error) {
	if cfg.Segments <= 0 || cfg.FramesPerSegment <= 0 {
		return 0, errors.New("demo: segments and frames-per-segment must be positive")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		cfg.Width, cfg.Height = 176, 144
	}
	if cfg.FPSNum <= 0 || cfg.FPSDen <= 0 {
		cfg.FPSNum, cfg.FPSDen = 25, 1
	}
	if cfg.Width%2 != 0 || cfg.Height%2 != 0 {
		return 0, fmt.Errorf("demo: dimensions must be even, got %dx%d", cfg.Width, cfg.Height)
	}
	f, err := os.Create(cfg.Path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := Header{Width: cfg.Width, Height: cfg.Height, FPSNum: cfg.FPSNum, FPSDen: cfg.FPSDen,
		Color: "C420", FrameSize: cfg.Width * cfg.Height * 3 / 2}
	if err := WriteHeader(f, h); err != nil {
		return 0, err
	}
	total := cfg.Segments * cfg.FramesPerSegment
	ySize := cfg.Width * cfg.Height
	frame := make([]byte, h.FrameSize)
	for fi := 0; fi < total; fi++ {
		seg := fi / cfg.FramesPerSegment
		noiseAmp := 6 + seg*4 // grows per segment: 6,10,14,...
		rng := lcg{state: uint32(fi*1000003+7) ^ 0x9e3779b9}
		for y := 0; y < cfg.Height; y++ {
			for x := 0; x < cfg.Width; x++ {
				v := (x*2 + y) / 3
				bandX := (fi*7 + seg*13) % cfg.Width
				if absInt(x-bandX) < 12 {
					v = 255 - v/2
				}
				blockY := (fi*(5+seg)) % cfg.Height
				if absInt(y-blockY) < 10 {
					v = (v + 128) & 0xff
				}
				if rng.next()%100 < 25 {
					n := int(rng.next() % uint32(noiseAmp*2+1))
					v = clamp(v + n - noiseAmp)
				}
				frame[y*cfg.Width+x] = byte(clamp(v))
			}
		}
		cb, cr := frame[ySize:ySize+ySize/4], frame[ySize+ySize/4:]
		for i := range cb {
			cb[i] = byte(128 + (i+fi*3)%32 - 16)
			cr[i] = byte(128 + (i*7+fi*5)%32 - 16)
		}
		if _, err := fmt.Fprintf(f, "FRAME\n"); err != nil {
			return 0, err
		}
		if _, err := f.Write(frame); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// FingerprintFile returns the hex SHA-256 of a file's contents.
func FingerprintFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EnsureSafePaths verifies that input and workspace are distinct existing
// targets so that processing can never overwrite the source file or escape
// through ".." components. Both are returned cleaned and absolute.
func EnsureSafePaths(input, workspace string) (string, string, error) {
	inAbs, err := filepath.Abs(filepath.Clean(input))
	if err != nil {
		return "", "", err
	}
	wsAbs, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return "", "", err
	}
	if inAbs == wsAbs {
		return "", "", errors.New("input path must differ from workspace")
	}
	info, err := os.Stat(inAbs)
	if err != nil {
		return "", "", fmt.Errorf("input: %w", err)
	}
	if info.IsDir() {
		return "", "", errors.New("input must be a regular file")
	}
	if resolved, err := filepath.EvalSymlinks(inAbs); err == nil {
		inAbs = resolved
	}
	if _, err := os.Stat(wsAbs); err == nil {
		if resolved, err := filepath.EvalSymlinks(wsAbs); err == nil {
			wsAbs = resolved
		}
	}
	if rel, err := filepath.Rel(wsAbs, inAbs); err == nil && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", errors.New("input must not live inside the workspace")
	}
	return inAbs, wsAbs, nil
}

// CountFrames streams a Y4M file and returns its validated frame count.
func CountFrames(path string) (Header, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, 0, err
	}
	defer f.Close()
	r, err := NewReader(f)
	if err != nil {
		return Header{}, 0, err
	}
	buf := make([]byte, r.Header().FrameSize)
	for {
		if err := r.NextFrame(buf); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Header{}, 0, err
		}
	}
	return r.Header(), r.FrameCount(), nil
}

// FrameBytes is a convenience used by tests: parse one whole in-memory Y4M.
func FrameBytes(data []byte) (Header, [][]byte, error) {
	r, err := NewReader(bytes.NewReader(data))
	if err != nil {
		return Header{}, nil, err
	}
	buf := make([]byte, r.Header().FrameSize)
	var out [][]byte
	for {
		if err := r.NextFrame(buf); err != nil {
			if errors.Is(err, io.EOF) {
				return r.Header(), out, nil
			}
			return Header{}, nil, err
		}
		cp := append([]byte(nil), buf...)
		out = append(out, cp)
	}
}
