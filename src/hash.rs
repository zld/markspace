//! SHA-256 frame hashing over a normalized byte layout.
//!
//! The hash input is: width (u32 LE) || height (u32 LE) || RGBA8 pixels
//! in row-major top-to-bottom order. Normalizing the byte order makes the
//! hash independent of how the frame was produced.

use crate::Frame;
use sha2::{Digest, Sha256};
use std::fmt::Write as _;

/// Hex-encoded SHA-256 of the normalized frame bytes.
pub fn frame_hash(frame: &Frame) -> String {
    let mut hasher = Sha256::new();
    hasher.update(frame.width().to_le_bytes());
    hasher.update(frame.height().to_le_bytes());
    hasher.update(frame.as_raw());
    let digest = hasher.finalize();

    let mut out = String::with_capacity(64);
    for b in digest {
        let _ = write!(out, "{b:02x}");
    }
    out
}
