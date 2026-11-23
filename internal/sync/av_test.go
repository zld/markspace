package sync

import (
	"testing"

	"mediad/internal/clock"
	"mediad/internal/model"
)

func mk(media model.MediaType, eventMs, arrivalMs int64, rtpTs uint32) *model.Event {
	return &model.Event{Media: media, EventMs: eventMs, ArrivalMs: arrivalMs, RTPTs: rtpTs}
}

// honestVideoAudioWithSkew feeds paired samples where video presentation is
// truly offset ahead of audio by skewMs and both senders use honest SRs.
func TestEstimateRecoversOffsetWithSR(t *testing.T) {
	const skew = 25
	est := NewEstimator()
	est.OnSenderReport(model.MediaVideo, 500, 0, 540) // 40ms one-way
	est.OnSenderReport(model.MediaAudio, 500, 0, 540)
	for ms := int64(0); ms < 1000; ms += 20 {
		vts := clock.RTPClockVideo.MillisToTicks(ms + skew)
		ats := clock.RTPClockAudio.MillisToTicks(ms)
		est.OnSample(mk(model.MediaVideo, ms+skew, ms+40, uint32(vts)))
		est.OnSample(mk(model.MediaAudio, ms, ms+40, uint32(ats)))
	}
	res := est.Estimate()
	if res.Confidence != "high" {
		t.Fatalf("confidence = %q", res.Confidence)
	}
	if got := res.OffsetMs; got != -skew {
		t.Fatalf("offset = %d, want %d", got, skew)
	}
	if res.Paired == 0 {
		t.Fatal("no pairs formed")
	}
	if res.OutOfTolSegs != 0 {
		t.Fatalf("out-of-tolerance segments = %d, want 0", res.OutOfTolSegs)
	}
}

func TestEstimateWithoutSRIsLowConfidenceAndBounded(t *testing.T) {
	est := NewEstimator()
	for ms := int64(0); ms < 500; ms += 20 {
		// Arrival lags presentation by a wild 4000ms; the fallback must
		// clip, not report the raw gap.
		est.OnSample(mk(model.MediaVideo, ms, ms+4000, uint32(ms*90)))
		est.OnSample(mk(model.MediaAudio, ms, ms+4000, uint32(ms*48)))
	}
	res := est.Estimate()
	if res.Confidence != "low" {
		t.Fatalf("confidence = %q, want low", res.Confidence)
	}
	if res.ErrAbs() > boundMs {
		t.Fatalf("error %d exceeded fallback bound %d", res.ErrAbs(), boundMs)
	}
}

func TestClockDriftEstimate(t *testing.T) {
	est := NewEstimator()
	// Sender clock gains 2ms per second (2000 ppm) against arrival time.
	for i, ms := range []int64{500, 1500, 2500} {
		drift := ms * 2000 / 1_000_000
		est.OnSenderReport(model.MediaAudio, ms+drift, uint32(clock.RTPClockAudio.MillisToTicks(ms)), ms+40)
		_ = i
	}
	est.OnSample(mk(model.MediaAudio, 500, 540, uint32(clock.RTPClockAudio.MillisToTicks(500))))
	est.OnSample(mk(model.MediaVideo, 525, 540, uint32(clock.RTPClockVideo.MillisToTicks(525))))
	res := est.Estimate()
	if res.DriftPPM != 2000 {
		t.Fatalf("drift = %d ppm, want 2000", res.DriftPPM)
	}
}

func TestNodeBiasIsRemovedFromOffset(t *testing.T) {
	est := NewEstimator()
	// Video node's clock reads 100ms low; the SR exposes the bias.
	est.OnSenderReport(model.MediaVideo, 400, 1000, 500)
	est.OnSenderReport(model.MediaAudio, 500, 0, 540)
	for ms := int64(0); ms < 400; ms += 20 {
		est.OnSample(mk(model.MediaVideo, ms, ms+40, 1000+uint32(clock.RTPClockVideo.MillisToTicks(ms))))
		est.OnSample(mk(model.MediaAudio, ms, ms+40, uint32(clock.RTPClockAudio.MillisToTicks(ms))))
	}
	res := est.Estimate()
	if res.ClockBiasMs == 0 {
		t.Fatal("node bias was not detected")
	}
	// With the bias removed the residual offset should be small.
	if res.ErrAbs() > ToleranceMs {
		t.Fatalf("bias-corrected offset = %d, want <= %d", res.OffsetMs, ToleranceMs)
	}
}

func TestMalformedAttributedToStream(t *testing.T) {
	est := NewEstimator()
	est.OnMalformed(model.MediaVideo)
	est.OnMalformed(model.MediaAudio)
	est.OnMalformed(model.MediaUnknown)
	if est.Malformed != 2 {
		t.Fatalf("malformed = %d, want 2", est.Malformed)
	}
}
