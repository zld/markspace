// Package control implements per-segment bitrate fitting and the QP search
// policy used to hit a target bitrate while tracking quality.
package control

import (
	"errors"
	"fmt"
	"math"

	"gonum.org/v1/gonum/mat"
)

// Sample is one measured candidate: quantizer, achieved bitrate (kbps) and
// mean luma PSNR in dB.
type Sample struct {
	QP          int
	BitrateKbps float64
	PSNRdB      float64
}

// LineModel is a univariate linear fit: y = Intercept + Slope*x.
type LineModel struct {
	Slope     float64
	Intercept float64
}

// Predict evaluates the model at x.
func (m LineModel) Predict(x float64) float64 { return m.Intercept + m.Slope*x }

// ErrInverted means the measured bitrate/QP relation has the wrong sign
// (higher QP did not reduce bitrate), so model-based fitting is unsafe.
var ErrInverted = errors.New("inverted bitrate/QP relation")

// FitLine solves the least-squares fit y = a + b*x via a Gonum QR
// factorization, returning an error when the system is degenerate.
func FitLine(xs, ys []float64) (LineModel, error) {
	if len(xs) != len(ys) || len(xs) < 2 {
		return LineModel{}, errors.New("fit: need at least two aligned samples")
	}
	n := len(xs)
	A := mat.NewDense(n, 2, nil)
	b := mat.NewVecDense(n, ys)
	for i, x := range xs {
		A.Set(i, 0, 1)
		A.Set(i, 1, x)
	}
	qr := new(mat.QR)
	qr.Factorize(A)
	var coef mat.VecDense
	if err := qr.SolveVecTo(&coef, false, b); err != nil {
		return LineModel{}, fmt.Errorf("fit: QR solve failed: %w", err)
	}
	return LineModel{Intercept: coef.AtVec(0), Slope: coef.AtVec(1)}, nil
}

// FitBitrate models log(bitrate) against QP: log(b) = a + m*qp.
// It returns ErrInverted when the fitted slope is non-negative (bitrate no
// longer decreases as QP rises), which invalidates the model-based search.
func FitBitrate(samples []Sample) (LineModel, error) {
	xs := make([]float64, len(samples))
	ys := make([]float64, len(samples))
	for i, s := range samples {
		if s.BitrateKbps <= 0 {
			return LineModel{}, fmt.Errorf("fit: non-positive bitrate at QP %d", s.QP)
		}
		xs[i] = float64(s.QP)
		ys[i] = math.Log(s.BitrateKbps)
	}
	model, err := FitLine(xs, ys)
	if err != nil {
		return LineModel{}, err
	}
	if model.Slope >= 0 {
		return model, ErrInverted
	}
	return model, nil
}

// PredictBitrate inverts the log-bitrate model for a target bitrate.
func PredictBitrate(model LineModel, targetKbps float64) int {
	if targetKbps <= 0 {
		return 0
	}
	qp := (math.Log(targetKbps) - model.Intercept) / model.Slope
	return int(math.Round(qp))
}

// FitQuality models PSNR as an affine function of QP for reporting.
func FitQuality(samples []Sample) (LineModel, error) {
	xs := make([]float64, len(samples))
	ys := make([]float64, len(samples))
	for i, s := range samples {
		xs[i] = float64(s.QP)
		ys[i] = s.PSNRdB
	}
	return FitLine(xs, ys)
}
