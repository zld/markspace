//! Deterministic test-frame generation with a fixed seed.
//!
//! Frames are smooth RGB gradients plus seeded per-pixel noise, with a
//! constant opaque alpha channel. The same (seed, frame_index, width,
//! height) always yields the same pixels, on every run.

use crate::{Frame, PipelineError, MAX_DIMENSION, MAX_FRAMES};

/// splitmix64-style bit mixer: deterministic, platform independent.
fn mix64(mut z: u64) -> u64 {
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

/// Per-pixel deterministic noise in [-16, 16].
fn noise(seed: u64, frame_index: u32, x: u32, y: u32) -> i32 {
    let z = seed
        ^ (frame_index as u64).wrapping_mul(0x9E37_79B9_7F4A_7C15)
        ^ ((x as u64) << 32)
        ^ (y as u64).wrapping_mul(0xC2B2_AE3D_27D4_EB4F);
    (mix64(z) % 33) as i32 - 16
}

fn clamp_u8(v: i32) -> u8 {
    v.clamp(0, 255) as u8
}

/// Generate the source frame for `frame_index`.
///
/// Validates dimensions (1..=4096 on each side) and the frame index
/// (0..1000). The channel layout is always RGBA8 and is re-checked when
/// the `Frame` is constructed.
pub fn generate_frame(
    seed: u64,
    frame_index: u32,
    width: u32,
    height: u32,
) -> Result<Frame, PipelineError> {
    if width == 0 || height == 0 || width > MAX_DIMENSION || height > MAX_DIMENSION {
        return Err(PipelineError::InvalidDimensions {
            width,
            height,
            reason: format!("each side must be within 1..={MAX_DIMENSION}"),
        });
    }
    if frame_index >= MAX_FRAMES {
        return Err(PipelineError::InvalidFrameIndex(frame_index));
    }

    let mut data = vec![0u8; (width as usize) * (height as usize) * 4];
    let x_den = width.saturating_sub(1).max(1) as i32;
    let y_den = height.saturating_sub(1).max(1) as i32;
    let d_den = (width.saturating_sub(1) + height.saturating_sub(1)).max(1) as i32;
    // Slow temporal drift so consecutive frames differ but stay comparable.
    let drift = ((frame_index as i32) * 7) % 25;

    for y in 0..height {
        for x in 0..width {
            let i = ((y * width + x) * 4) as usize;
            let n = noise(seed, frame_index, x, y);
            let r = (x as i32 * 255 / x_den) + n + drift;
            let g = (y as i32 * 255 / y_den) + n - drift;
            let b = ((x as i32 + y as i32) * 255 / d_den) - n;
            data[i] = clamp_u8(r);
            data[i + 1] = clamp_u8(g);
            data[i + 2] = clamp_u8(b);
            data[i + 3] = 255; // alpha preserved by all passes
        }
    }
    Frame::from_raw(width, height, data)
}
