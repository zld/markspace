//! Per-pass pixel statistics.
//!
//! Compares the committed frame before and after a pass. Differences are
//! accumulated over the RGB channels only; alpha is preserved by all
//! passes and its changed-pixel count is reported separately. Counters
//! are wide enough (u64) for 4096x4096 frames at full 255 deltas.

use crate::{Frame, PipelineError};
use serde::Serialize;

#[derive(Debug, Clone, Copy, PartialEq, Serialize)]
pub struct PassMetrics {
    /// Sum of absolute per-channel differences over R, G and B.
    pub total_abs_diff: u64,
    /// Pixels where at least one RGB channel changed.
    pub changed_pixels: u64,
    /// Largest single-channel absolute difference.
    pub max_channel_diff: u32,
    /// `total_abs_diff / (pixel_count * 3)`.
    pub mean_abs_diff: f64,
    /// Pixels whose alpha channel changed (passes preserve alpha, so
    /// this is normally zero and reported separately on purpose).
    pub alpha_changed_pixels: u64,
}

/// Compare two frames of identical dimensions.
pub fn compare(before: &Frame, after: &Frame) -> Result<PassMetrics, PipelineError> {
    if before.width() != after.width() || before.height() != after.height() {
        return Err(PipelineError::DimensionMismatch {
            a_w: before.width(),
            a_h: before.height(),
            b_w: after.width(),
            b_h: after.height(),
        });
    }
    let a = before.as_raw();
    let b = after.as_raw();

    let mut total_abs_diff: u64 = 0;
    let mut changed_pixels: u64 = 0;
    let mut max_channel_diff: u32 = 0;
    let mut alpha_changed_pixels: u64 = 0;

    for (pa, pb) in a.chunks_exact(4).zip(b.chunks_exact(4)) {
        let mut changed = false;
        for c in 0..3 {
            let d = (pa[c] as i32 - pb[c] as i32).unsigned_abs();
            total_abs_diff += d as u64;
            max_channel_diff = max_channel_diff.max(d);
            changed |= d != 0;
        }
        if changed {
            changed_pixels += 1;
        }
        if pa[3] != pb[3] {
            alpha_changed_pixels += 1;
        }
    }

    let pixel_count = (before.width() as u64) * (before.height() as u64);
    let mean_abs_diff = if pixel_count == 0 {
        0.0
    } else {
        total_abs_diff as f64 / (pixel_count * 3) as f64
    };

    Ok(PassMetrics {
        total_abs_diff,
        changed_pixels,
        max_channel_diff,
        mean_abs_diff,
        alpha_changed_pixels,
    })
}
