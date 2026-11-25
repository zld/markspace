package ts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"

	merr "mediapack/internal/errors"
)

// Digester computes a canonical structural summary of parsed TS packets.
// Raw payloads are reduced to their own SHA-256 so the digest captures media
// content without becoming a trivial whole-range byte hash.
type Digester struct {
	h hash.Hash
}

// NewDigester returns a streaming structural digester.
func NewDigester() *Digester { return &Digester{h: sha256.New()} }

// WritePacket folds one parsed packet into the digest.
func (d *Digester) WritePacket(p *Packet) {
	if p == nil {
		return
	}
	ph := sha256.Sum256(p.Payload)
	pts, dts := uint64(0), uint64(0)
	hasPTS, hasDTS := false, false
	if p.PES != nil {
		pts, dts, hasPTS, hasDTS = p.PES.PTS, p.PES.DTS, p.PES.HasPTS, p.PES.HasDTS
	}
	fmt.Fprintf(d.h,
		"pkt=v1 idx=%d off=%d sync=%v pid=%d pusi=%v cc=%d afc=%d pcrf=%v pcr=%d pts=%d(%v) dts=%d(%v) ph=%x\n",
		p.Index, p.Offset, p.SyncOK, p.PID, p.PUSI, p.CC, p.AFC,
		p.PCRFlag, p.PCRBase, pts, hasPTS, dts, hasDTS, ph)
}

// Sum returns the hex digest.
func (d *Digester) Sum() string { return hex.EncodeToString(d.h.Sum(nil)) }

// DigestRange parses length bytes from r starting at absolute baseOffset and
// returns the structural digest and number of full packets. A trailing partial
// packet is reported through issues but does not fail the digest.
func DigestRange(r io.Reader, baseOffset, length int64, issues *merr.Book) (string, int, error) {
	rd := NewReader(io.LimitReader(r, length), baseOffset, issues)
	d := NewDigester()
	n := 0
	for {
		p, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err == ErrTruncated {
			break
		}
		if err != nil {
			return "", n, err
		}
		d.WritePacket(p)
		n++
	}
	return d.Sum(), n, nil
}
