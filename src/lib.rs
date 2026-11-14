//! postpipe — deterministic offline CPU image post-processing pipeline.
//!
//! Exposes the pipeline (`chain`), image (`Frame`), statistics (`metrics`)
//! and error (`PipelineError`) types so the CLI and integration tests share
//! one implementation. Passes never depend on the CLI or metrics modules.

pub mod chain;
pub mod config;
pub mod hash;
pub mod image_source;
pub mod metrics;
pub mod pass;

use thiserror::Error;

/// Maximum frame side length accepted by the pipeline.
pub const MAX_DIMENSION: u32 = 4096;
/// Maximum number of frames in one run.
pub const MAX_FRAMES: u32 = 1000;
/// Maximum number of passes in one chain.
pub const MAX_PASSES: usize = 64;

/// Errors produced by any stage of the pipeline.
#[derive(Debug, Error)]
pub enum PipelineError {
    #[error("invalid dimensions {width}x{height}: {reason}")]
    InvalidDimensions {
        width: u32,
        height: u32,
        reason: String,
    },
    #[error("zero-sized frame cannot be processed")]
    ZeroSizedFrame,
    #[error("unsupported channel layout: {0}")]
    UnsupportedChannelLayout(String),
    #[error("invalid frame index {0}")]
    InvalidFrameIndex(u32),
    #[error("pass `{name}` failed: {message}")]
    PassFailed { name: String, message: String },
    #[error("chain interrupted before pass index {0}")]
    Interrupted(usize),
    #[error("frame dimension mismatch: {a_w}x{a_h} vs {b_w}x{b_h}")]
    DimensionMismatch {
        a_w: u32,
        a_h: u32,
        b_w: u32,
        b_h: u32,
    },
    #[error("invalid configuration: {0}")]
    Config(String),
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),
}

/// One RGBA8 frame, row-major, 4 bytes per pixel (R, G, B, A).
///
/// Backed by `image::RgbaImage`, which guarantees the channel layout.
/// Zero-sized frames may exist as values so that passes can reject them
/// with an identifiable error; the frame source never produces them.
#[derive(Debug, Clone)]
pub struct Frame {
    img: image::RgbaImage,
}

impl Frame {
    /// Build a frame from raw RGBA bytes. The buffer length must be exactly
    /// `width * height * 4`; anything else is an unsupported channel layout.
    pub fn from_raw(width: u32, height: u32, data: Vec<u8>) -> Result<Self, PipelineError> {
        let expected = width as u64 * height as u64 * 4;
        if data.len() as u64 != expected {
            return Err(PipelineError::UnsupportedChannelLayout(format!(
                "expected {expected} RGBA bytes for {width}x{height}, got {}",
                data.len()
            )));
        }
        let img = image::RgbaImage::from_raw(width, height, data).ok_or_else(|| {
            PipelineError::UnsupportedChannelLayout(format!(
                "buffer rejected for {width}x{height} RGBA8"
            ))
        })?;
        Ok(Self { img })
    }

    pub fn width(&self) -> u32 {
        self.img.width()
    }

    pub fn height(&self) -> u32 {
        self.img.height()
    }

    pub fn is_zero_sized(&self) -> bool {
        self.img.width() == 0 || self.img.height() == 0
    }

    /// Normalized pixel bytes: RGBA8, row-major, top-to-bottom.
    pub fn as_raw(&self) -> &[u8] {
        self.img.as_raw()
    }

    pub fn as_raw_mut(&mut self) -> &mut [u8] {
        self.img.as_mut()
    }

    pub fn into_image(self) -> image::RgbaImage {
        self.img
    }
}
