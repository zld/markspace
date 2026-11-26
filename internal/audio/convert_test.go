package audio

import (
	"context"
	"os/exec"
	"testing"
)

// genADTS synthesizes a 440 Hz sine and encodes it as AAC ADTS via ffmpeg.
// Generator output is local and deterministic for fixed parameters.
func genADTS(t *testing.T, rate int, channels, durSecs int) []byte {
	t.Helper()
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i",
		"sine=frequency=440:sample_rate=" + itoa(rate) +
			":duration=" + itoa(durSecs),
		"-ac", itoa(channels),
		"-c:a", "aac", "-b:a", "96k", "-f", "adts", "-y", "pipe:1",
	}
	cmd := exec.CommandContext(context.Background(), "ffmpeg", args...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("fixture ffmpeg failed: %v", err)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestProcessNormalizesSpecAndIsDeterministic(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	// stereo 24 kHz input must become mono 48 kHz
	input := genADTS(t, 24000, 2, 2)
	c, err := NewConverter(TargetBitrate)
	if err != nil {
		t.Fatal(err)
	}
	seg := Segment{ID: 1, StartNanos: 0, Data: input}
	r1, err := c.Process(context.Background(), seg)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	// AAC priming/padding adds a small fixed number of frames (encoder delay);
	// allow up to four 1024-sample frames around the nominal 2 s count. The
	// timeline itself is placed by PTS, not by this raw sample total.
	nominal := TargetRate * 2
	if d := r1.Samples - nominal; d < -1024 || d > 4096 {
		t.Fatalf("samples = %d, want ~%d within AAC priming tolerance",
			r1.Samples, nominal)
	}
	if len(r1.PCM) != r1.Samples*pcmBytesPerSample {
		t.Fatalf("PCM must be mono S16LE: %d bytes", len(r1.PCM))
	}
	if len(r1.AAC) == 0 {
		t.Fatal("AAC output empty")
	}
	// rerun must produce byte-identical PCM and hash
	r2, err := c.Process(context.Background(), seg)
	if err != nil {
		t.Fatal(err)
	}
	if r1.PCMSHA256 != r2.PCMSHA256 {
		t.Fatal("PCM hash must be stable across runs")
	}
	if string(r1.PCM) != string(r2.PCM) {
		t.Fatal("PCM bytes must be deterministic")
	}
}

func TestMeasurements(t *testing.T) {
	pcm := make([]byte, TargetRate*pcmBytesPerSample) // 1 s of silence
	if tp := TruePeakDBTP(pcm); !isInfNeg(tp) {
		t.Fatalf("silence true peak must be -Inf, got %f", tp)
	}
	if l := LUFS(pcm); l > -60 {
		t.Fatalf("silence LUFS = %f, want <= -60", l)
	}
	// a sub-full-scale tone must report a finite negative peak, and a
	// louder tone must have a higher (less negative) peak than a quieter one
	quiet := make([]byte, TargetRate*pcmBytesPerSample)
	for i := 0; i < TargetRate; i++ {
		putS16(quiet[2*i:], 8000)
	}
	loud := make([]byte, TargetRate*pcmBytesPerSample)
	for i := 0; i < TargetRate; i++ {
		putS16(loud[2*i:], 24000)
	}
	tpQuiet := TruePeakDBTP(quiet)
	tpLoud := TruePeakDBTP(loud)
	if !(tpLoud > tpQuiet) || tpLoud >= 0 {
		t.Fatalf("peak ordering wrong: quiet=%f loud=%f", tpQuiet, tpLoud)
	}
	// identical reference/candidate => maximal SNR
	snr := SNRDB(pcm, pcm)
	if snr != 99.0 {
		t.Fatalf("identical SNR = %f, want sentinel 99", snr)
	}
	// bitrate math: 12000 bytes over 1 s => 96000 bps
	br := BitrateActual(12000, 1_000_000_000)
	if br != 96000 {
		t.Fatalf("actual bitrate = %d", br)
	}
	delta := BitrateDeltaPct(100800, 96000) // +5%
	if delta < 4.99 || delta > 5.01 {
		t.Fatalf("delta pct = %f", delta)
	}
}

func isInfNeg(f float64) bool { return f != f || f < -1e300 }

func TestSNRDetectsDegradation(t *testing.T) {
	// build a low-amplitude tone and a noisy copy directly in PCM
	ref := make([]byte, 4800*pcmBytesPerSample) // 100 ms
	for i := 0; i < 4800; i++ {
		v := int16(8000)
		putS16(ref[2*i:], v)
	}
	noisy := make([]byte, len(ref))
	for i := 0; i < 4800; i++ {
		putS16(noisy[2*i:], int16(8000+(i%7*300-900)))
	}
	good := SNRDB(ref, ref)
	bad := SNRDB(ref, noisy)
	if !(good > bad) {
		t.Fatalf("SNR should rank clean above noisy: good=%f bad=%f", good, bad)
	}
	if bad < 0 {
		t.Fatalf("mild noise SNR should stay positive: %f", bad)
	}
}

func TestNewConverterRejectsBadBitrate(t *testing.T) {
	if _, err := NewConverter(0); err == nil {
		t.Fatal("zero bitrate must error")
	}
}

func putS16(b []byte, v int16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}
