package fixture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/model"
	"mediad/internal/ts"
)

func TestGenerateCleanStreamParsesAndWrapsPCR(t *testing.T) {
	spec := DefaultSpec()
	st, err := Generate(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Bytes)%model.PacketSize != 0 {
		t.Fatalf("clean stream length %d not a multiple of 188", len(st.Bytes))
	}
	r := ts.NewReader(bytes.NewReader(st.Bytes))
	psi := ts.NewPSITracker()
	cc := ts.NewContinuityTracker()
	tl := clock.NewTimeline()
	wraps := 0
	pcrPackets := 0
	for {
		p, err := r.ReadPacket(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		if g := cc.Observe(p); g != nil && !g.Repeated {
			t.Fatalf("unexpected continuity gap: %+v", g)
		}
		if _, err := psi.Observe(p); err != nil {
			t.Fatalf("PSI error: %v", err)
		}
		if p.HasPCR {
			pcrPackets++
			if _, wrapped, err := tl.FeedPCR(p.PCR, p.Offset); err != nil {
				t.Fatalf("PCR error: %v", err)
			} else if wrapped {
				wraps++
			}
		}
	}
	if wraps == 0 {
		t.Fatal("expected at least one PCR wraparound in the generated stream")
	}
	if pcrPackets == 0 {
		t.Fatal("stream contains no PCR packets")
	}
	if len(psi.Events) < 3 { // PAT v0, PMT v0, PMT v1
		t.Fatalf("expected PSI version events, got %+v", psi.Events)
	}
	// After the PMT version switch the new elementary PIDs must be mapped.
	if !psi.IsMapped(vidPID0+0x10) {
		t.Fatal("updated PMT did not map the new elementary PID")
	}
}

func withinOnePacket(a, b int64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= model.PacketSize
}

func TestCorruptionOffsetsAndTruncation(t *testing.T) {
	cases := []struct {
		mode string
		pid  uint16
	}{
		{CorruptSync, 0},
		{CorruptTruncated, 0},
		{CorruptContinuity, vidPID0},
		{CorruptPCR, pcrPID0},
		{CorruptPSI, pmtPID},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			spec := DefaultSpec()
			spec.Corrupt = tc.mode
			st, err := Generate(spec)
			if err != nil {
				t.Fatal(err)
			}
			if st.CorruptionOffset < 0 {
				t.Fatal("generator did not report a corruption offset")
			}
			// The reported offset must be packet aligned (or the partial
			// tail start for truncation).
			if st.CorruptionOffset%model.PacketSize != 0 {
				t.Fatalf("offset %d not packet aligned", st.CorruptionOffset)
			}
			r := ts.NewReader(bytes.NewReader(st.Bytes))
			ct := ts.NewContinuityTracker()
			pt := ts.NewPSITracker()
			tl := clock.NewTimeline()
			var sawFailure bool
			for {
				p, err := r.ReadPacket(context.Background())
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					var ce *model.CorruptError
					if errors.As(err, &ce) {
						delta := ce.Offset - st.CorruptionOffset
						if delta < 0 {
							delta = -delta
						}
						if delta > model.PacketSize {
							t.Fatalf("%s: reported offset %d differs from generator %d by %d",
								tc.mode, ce.Offset, st.CorruptionOffset, delta)
						}
						sawFailure = true
					}
					break
				}
				// Defects that keep the sync byte intact are detected by
				// the specialist trackers rather than the packet reader.
				if g := ct.Observe(p); g != nil && !g.Repeated {
					if withinOnePacket(g.Offset, st.CorruptionOffset) {
						sawFailure = true
					}
				}
				if ev, err := pt.Observe(p); err != nil {
					var ce *model.CorruptError
					if errors.As(err, &ce) && withinOnePacket(ce.Offset, st.CorruptionOffset) {
						sawFailure = true
					}
				} else if ev != nil && ev.Kind == "pmt" {
					_ = ev
				}
				if p.HasPCR {
					if _, _, err := tl.FeedPCR(p.PCR, p.Offset); err != nil {
						var ce *model.CorruptError
						if errors.As(err, &ce) && withinOnePacket(ce.Offset, st.CorruptionOffset) {
							sawFailure = true
						}
					}
				}
			}
			if !sawFailure {
				t.Fatalf("%s corruption was not detected", tc.mode)
			}
		})
	}
}
