package main

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"io"
	"strings"

	mpierr "mediapipeline/internal/errors"
	"mediapipeline/internal/fixture"
	mediartp "mediapipeline/internal/rtp"
)

// fixtureOffset is the frame-payload byte chosen for corruption. Using the
// exported frame header size plus a fixed index keeps the injection point
// independent of RTP header layout details.
func fixtureOffset() int {
	return mediartp.FrameHeaderSize + 3
}

// checkCorruption runs the verifying parser over a corrupted stream and
// asserts that the reported offset matches the injected byte exactly, i.e.
// the localization error is zero bytes (within the required <=1 byte bound).
func (v *verifier) checkCorruption(raw []byte, injected int64) error {
	rd := mediartp.NewReader(bytes.NewReader(raw), fixture.OpusRate)
	for {
		_, err := rd.Read()
		if err == nil {
			continue
		}
		if stderrors.Is(err, io.EOF) {
			return stderrors.New("corruption was never detected before EOF")
		}
		var me *mpierr.MediaError
		if !stderrors.As(err, &me) {
			return err
		}
		diff := me.ByteOffset - injected
		if diff < 0 {
			diff = -diff
		}
		if diff > 1 {
			return fmt.Errorf("corruption localization error %d bytes exceeds 1-byte bound", diff)
		}
		if me.Category != mpierr.CatCorrupt {
			return stderrors.New("corrupted input not classified as CatCorrupt")
		}
		return nil
	}
}

// checkRollover parses a stream whose sequence numbers cross 65535->0 and
// asserts the extended sequence numbers stay monotonic across the boundary,
// with the injected duplicate recognized but not re-timed.
func (v *verifier) checkRollover(raw []byte, meta fixture.Meta) error {
	rd := mediartp.NewReader(bytes.NewReader(raw), fixture.OpusRate)
	var ext []uint64
	dups := 0
	for {
		f, err := rd.Read()
		if stderrors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		ext = append(ext, f.ExtSeq)
		if f.Duplicate {
			dups++
		}
	}
	if len(ext) != meta.Packets+meta.Duplicates {
		return fmt.Errorf("rollover: parsed %d packets, want %d",
			len(ext), meta.Packets+meta.Duplicates)
	}
	// Logical packets 65532..65539, with seq 65534 repeated once on the wire.
	want := []uint64{65532, 65533, 65534, 65534, 65535, 65536, 65537, 65538, 65539}
	if len(ext) != len(want) {
		return fmt.Errorf("rollover: got %d ext seqs, want %d", len(ext), len(want))
	}
	for i := range want {
		if ext[i] != want[i] {
			return fmt.Errorf("rollover: ext[%d] = %d, want %d", i, ext[i], want[i])
		}
	}
	if dups != 1 {
		return fmt.Errorf("rollover: duplicates detected %d, want 1", dups)
	}
	return nil
}

// checkTruncation parses a stream cut mid-packet and asserts a CatTruncated
// error is raised at or before the cut point, with a valid byte offset.
func (v *verifier) checkTruncation(raw []byte, cutAt int64) error {
	rd := mediartp.NewReader(bytes.NewReader(raw), fixture.OpusRate)
	for {
		_, err := rd.Read()
		if err == nil {
			continue
		}
		if stderrors.Is(err, io.EOF) {
			return stderrors.New("truncation: clean EOF instead of a truncation error")
		}
		var me *mpierr.MediaError
		if !stderrors.As(err, &me) {
			return err
		}
		if me.Category != mpierr.CatTruncated {
			return fmt.Errorf("truncation: category = %s, want truncated", me.Category)
		}
		if me.ByteOffset < 0 || me.ByteOffset > cutAt {
			return fmt.Errorf("truncation: offset %d outside [0,%d]", me.ByteOffset, cutAt)
		}
		return nil
	}
}

// joinOrEmpty renders a node list, or the literal <none> when empty.
func joinOrEmpty(nodes []string) string {
	if len(nodes) == 0 {
		return "<none>"
	}
	return strings.Join(nodes, ",")
}
