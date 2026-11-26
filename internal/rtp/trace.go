// Package rtp parses recorded RTP traces, restores packet order across loss,
// reordering and 32-bit timestamp wrap, and maps increasing RTP time to the
// common media time base through explicit source anchors.
package rtp

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rtx/internal/model"

	pionrtp "github.com/pion/rtp"
)

// Packet is one parsed RTP datagram from a trace. Raw keeps the original
// header+payload bytes so payload digests cover exactly what was recorded.
type Packet struct {
	Index   int    `json:"index"`
	Source  string `json:"source"`
	SSRC    uint32 `json:"ssrc"`
	Payload uint8  `json:"payload_type"`
	Seq     uint16 `json:"seq"`
	TS      uint32 `json:"timestamp"`
	Marker  bool   `json:"marker"`
	Raw     []byte `json:"raw"`
}

// Packet size limits. A trace line is a single RTP datagram; anything larger
// is treated as malformed input rather than silently truncated.
const (
	MaxPacketBytes = 64 << 10
	MaxTraceBytes  = 64 << 20
)

// Trace is a parsed recording of one or more RTP sources.
type Trace struct {
	Path    string
	Packets []Packet
}

// traceLine is the on-disk JSON shape. The raw datagram can arrive as hex or
// base64; both are accepted because captures from different tools differ.
type traceLine struct {
	Source     string `json:"source"`
	SSRC       uint32 `json:"ssrc"`
	Payload    uint8  `json:"payload_type"`
	Seq        uint16 `json:"seq"`
	Timestamp  uint32 `json:"timestamp"`
	Marker     bool   `json:"marker"`
	RawHex     string `json:"raw_hex"`
	RawBase64  string `json:"raw_base64"`
	HeaderOnly bool   `json:"header_only"`
}

// LoadTrace reads a newline-delimited JSON trace and cross-checks the parsed
// RTP headers against the declared fields. A mismatch means the trace was
// edited or corrupted, which must fail loudly rather than repair silently.
func LoadTrace(path string) (*Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.E(CodeRTPTrace, "open trace", "%v", err).WithPath(path).WithCause(err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, model.E(CodeRTPTrace, "stat trace", "%v", err).WithPath(path)
	}
	if info.Size() > MaxTraceBytes {
		return nil, model.E(CodeRTPTrace, "read trace", "trace is %d bytes, limit %d", info.Size(), MaxTraceBytes).WithPath(path)
	}

	tr := &Trace{Path: path}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), MaxTraceBytes)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		var tl traceLine
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&tl); err != nil {
			return nil, model.E(CodeRTPTrace, "parse trace", "line %d: %v", lineNo, err).WithPath(path)
		}
		raw, err := decodeRaw(tl)
		if err != nil {
			return nil, model.E(CodeRTPTrace, "parse trace", "line %d: %v", lineNo, err).WithPath(path)
		}
		if len(raw) > MaxPacketBytes {
			return nil, model.E(CodeRTPTrace, "parse trace", "line %d: packet %d bytes exceeds %d", lineNo, len(raw), MaxPacketBytes).WithPath(path)
		}
		p := Packet{Source: tl.Source, Marker: tl.Marker, Raw: raw}
		var hdr pionrtp.Packet
		if err := hdr.Unmarshal(raw); err != nil {
			return nil, model.E(CodeRTPTrace, "parse rtp", "line %d: %v", lineNo, err).WithPath(path).WithCause(err)
		}
		if hdr.Version != 2 {
			return nil, model.E(CodeRTPTrace, "parse rtp", "line %d: version %d, want 2", lineNo, hdr.Version).WithPath(path)
		}
		p.SSRC, p.Seq, p.TS, p.Marker = hdr.SSRC, hdr.SequenceNumber, hdr.Timestamp, hdr.Marker
		p.Payload = hdr.PayloadType
		if err := checkDeclared(tl, p, lineNo); err != nil {
			return nil, err.WithPath(path)
		}
		if p.Source == "" {
			p.Source = fmt.Sprintf("ssrc-%08x", p.SSRC)
		}
		p.Index = len(tr.Packets)
		tr.Packets = append(tr.Packets, p)
	}
	if err := sc.Err(); err != nil {
		return nil, model.E(CodeRTPTrace, "read trace", "line %d: %v", lineNo+1, err).WithPath(path)
	}
	if len(tr.Packets) == 0 {
		return nil, model.E(CodeRTPTrace, "read trace", "trace contains no packets").WithPath(path)
	}
	bySource := map[string]map[uint32]bool{}
	for _, p := range tr.Packets {
		if _, ok := bySource[p.Source]; !ok {
			bySource[p.Source] = map[uint32]bool{}
		}
		bySource[p.Source][p.SSRC] = true
		if len(bySource[p.Source]) > 1 {
			return nil, model.E(CodeRTPTrace, "verify trace",
				"source %s contains more than one SSRC", p.Source).WithPath(path)
		}
	}
	return tr, nil
}

func decodeRaw(tl traceLine) ([]byte, error) {
	switch {
	case tl.RawHex != "" && tl.RawBase64 != "":
		return nil, fmt.Errorf("both raw_hex and raw_base64 set")
	case tl.RawHex != "":
		b, err := hex.DecodeString(strings.ReplaceAll(tl.RawHex, " ", ""))
		if err != nil {
			return nil, fmt.Errorf("bad raw_hex: %v", err)
		}
		return b, nil
	case tl.RawBase64 != "":
		b, err := decodeBase64(tl.RawBase64)
		if err != nil {
			return nil, fmt.Errorf("bad raw_base64: %v", err)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("missing raw_hex or raw_base64")
	}
}

func checkDeclared(tl traceLine, p Packet, lineNo int) *model.Err {
	if tl.Seq != p.Seq {
		return model.E(CodeRTPTrace, "verify trace", "line %d: declared seq %d but header says %d", lineNo, tl.Seq, p.Seq)
	}
	if tl.Timestamp != p.TS {
		return model.E(CodeRTPTrace, "verify trace", "line %d: declared timestamp %d but header says %d", lineNo, tl.Timestamp, p.TS)
	}
	if tl.SSRC != 0 && tl.SSRC != p.SSRC {
		return model.E(CodeRTPTrace, "verify trace", "line %d: declared ssrc %d but header says %d", lineNo, tl.SSRC, p.SSRC)
	}
	if tl.Payload != 0 && tl.Payload != p.Payload {
		return model.E(CodeRTPTrace, "verify trace", "line %d: declared payload type %d but header says %d", lineNo, tl.Payload, p.Payload)
	}
	return nil
}

// Sources lists distinct source identifiers in first-seen order.
func (t *Trace) Sources() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range t.Packets {
		if !seen[p.Source] {
			seen[p.Source] = true
			out = append(out, p.Source)
		}
	}
	return out
}

// WriteTrace serializes packets back to the trace format. Fixture generation
// uses it; tests round-trip through it to prove parsing is lossless.
func WriteTrace(w io.Writer, packets []Packet) error {
	bw := bufio.NewWriter(w)
	for _, p := range packets {
		line := traceLine{
			Source:    p.Source,
			SSRC:      p.SSRC,
			Payload:   p.Payload,
			Seq:       p.Seq,
			Timestamp: p.TS,
			Marker:    p.Marker,
			RawHex:    hex.EncodeToString(p.Raw),
		}
		b, err := json.Marshal(line)
		if err != nil {
			return err
		}
		if _, err := bw.Write(b); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// WriteTraceFile is WriteTrace with directory creation.
func WriteTraceFile(dir, name string, packets []Packet) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".trace-*")
	if err != nil {
		return err
	}
	if err := WriteTrace(tmp, packets); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
