//! Reusable pass interface and the built-in pass implementations.
//!
//! A pass is pure: it reads one frame and produces a new candidate frame.
//! It never mutates its input, never touches the alpha channel, and knows
//! nothing about the CLI, the chain manager or the metrics module.

use crate::{Frame, PipelineError};

/// Accepted parameter ranges (inclusive), enforced at construction time.
pub const BRIGHTNESS_RANGE: (f64, f64) = (-255.0, 255.0);
pub const SHARPEN_RANGE: (f64, f64) = (0.0, 4.0);
pub const GAMMA_RANGE: (f64, f64) = (0.05, 8.0);

/// A reusable image processing pass.
pub trait Pass {
    fn name(&self) -> &str;
    /// Produce the candidate output frame for `input`.
    fn apply(&self, input: &Frame) -> Result<Frame, PipelineError>;
}

fn reject_zero_sized(input: &Frame) -> Result<(), PipelineError> {
    if input.is_zero_sized() {
        return Err(PipelineError::ZeroSizedFrame);
    }
    Ok(())
}

fn check_finite_range(param: &str, value: f64, range: (f64, f64)) -> Result<(), PipelineError> {
    if !value.is_finite() || value < range.0 || value > range.1 {
        return Err(PipelineError::Config(format!(
            "{param} value {value} is not finite or outside [{}, {}]",
            range.0, range.1
        )));
    }
    Ok(())
}

fn map_channel(v: u8, f: impl Fn(f64) -> f64) -> u8 {
    f(v as f64).round().clamp(0.0, 255.0) as u8
}

/// Additive brightness offset applied to RGB channels.
pub struct BrightnessPass {
    name: String,
    offset: f64,
}

impl BrightnessPass {
    pub fn new(name: impl Into<String>, offset: f64) -> Result<Self, PipelineError> {
        check_finite_range("brightness offset", offset, BRIGHTNESS_RANGE)?;
        Ok(Self {
            name: name.into(),
            offset,
        })
    }
}

impl Pass for BrightnessPass {
    fn name(&self) -> &str {
        &self.name
    }

    fn apply(&self, input: &Frame) -> Result<Frame, PipelineError> {
        reject_zero_sized(input)?;
        let mut out = input.clone();
        for px in out.as_raw_mut().chunks_exact_mut(4) {
            for c in px.iter_mut().take(3) {
                *c = map_channel(*c, |v| v + self.offset);
            }
        }
        Ok(out)
    }
}

/// 3x3 sharpen convolution with edge replication (clamp-to-edge).
///
/// Kernel: center `1 + 4a`, direct neighbours `-a`, diagonals `0`.
/// Handles 1x1, 1xN and Nx1 frames; on a 1x1 frame it is the identity.
pub struct SharpenPass {
    name: String,
    amount: f64,
}

impl SharpenPass {
    pub fn new(name: impl Into<String>, amount: f64) -> Result<Self, PipelineError> {
        check_finite_range("sharpen amount", amount, SHARPEN_RANGE)?;
        Ok(Self {
            name: name.into(),
            amount,
        })
    }
}

impl Pass for SharpenPass {
    fn name(&self) -> &str {
        &self.name
    }

    fn apply(&self, input: &Frame) -> Result<Frame, PipelineError> {
        reject_zero_sized(input)?;
        let w = input.width() as usize;
        let h = input.height() as usize;
        let src = input.as_raw();
        let mut out = vec![0u8; src.len()];
        let a = self.amount;

        for y in 0..h {
            let ym = y.saturating_sub(1);
            let yp = (y + 1).min(h - 1);
            for x in 0..w {
                let xm = x.saturating_sub(1);
                let xp = (x + 1).min(w - 1);
                let i = (y * w + x) * 4;
                for c in 0..3 {
                    let center = src[i + c] as f64;
                    let sum = src[(y * w + xm) * 4 + c] as f64
                        + src[(y * w + xp) * 4 + c] as f64
                        + src[(ym * w + x) * 4 + c] as f64
                        + src[(yp * w + x) * 4 + c] as f64;
                    out[i + c] = map_channel(0, |_| center * (1.0 + 4.0 * a) - a * sum);
                }
                out[i + 3] = src[i + 3]; // alpha passthrough
            }
        }
        Frame::from_raw(input.width(), input.height(), out)
    }
}

/// Gamma correction: `out = 255 * (in / 255)^(1/gamma)`, via a fixed LUT.
pub struct GammaPass {
    name: String,
    gamma: f64,
}

impl GammaPass {
    pub fn new(name: impl Into<String>, gamma: f64) -> Result<Self, PipelineError> {
        check_finite_range("gamma", gamma, GAMMA_RANGE)?;
        Ok(Self {
            name: name.into(),
            gamma,
        })
    }

    fn lut(&self) -> [u8; 256] {
        let inv = 1.0 / self.gamma;
        let mut lut = [0u8; 256];
        for (i, slot) in lut.iter_mut().enumerate() {
            *slot = map_channel(i as u8, |v| 255.0 * (v / 255.0).powf(inv));
        }
        lut
    }
}

impl Pass for GammaPass {
    fn name(&self) -> &str {
        &self.name
    }

    fn apply(&self, input: &Frame) -> Result<Frame, PipelineError> {
        reject_zero_sized(input)?;
        let lut = self.lut();
        let mut out = input.clone();
        for px in out.as_raw_mut().chunks_exact_mut(4) {
            for c in px.iter_mut().take(3) {
                *c = lut[*c as usize];
            }
        }
        Ok(out)
    }
}
