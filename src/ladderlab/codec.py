"""Deterministic research codec: block quantization + zlib.

This is NOT a delivery encoder. It produces a reproducible byte stream so
that parameter combinations can be compared on equal footing:

  1. spatial downsample by block-averaging (scale factor s)
  2. per-frame residual against the previous reconstructed frame,
     unless the frame index falls on the keyframe interval
  3. uniform quantization with step q (round-to-nearest)
  4. zlib compression of the concatenated int16 coefficient stream

Bitrate is computed from the actual serialized byte count.
"""
from __future__ import annotations

import hashlib
import json
import struct
import zlib
from dataclasses import dataclass

import numpy as np

CODEC_VERSION = 1
MAGIC = b"LLAB"

QSTEPS = (3, 6, 12, 24, 48, 96)
SCALES = (1, 2, 4)
KEYINTS = (12, 24)

MAX_PIXELS = 4096 * 4096  # local-input guard


@dataclass(frozen=True, order=True)
class Params:
    qstep: int
    scale: int
    keyint: int

    @property
    def key(self) -> str:
        return f"q{self.qstep}-s{self.scale}-k{self.keyint}"

    @staticmethod
    def from_key(key: str) -> "Params":
        q, s, k = key.split("-")
        return Params(int(q[1:]), int(s[1:]), int(k[1:]))


def iter_params() -> list[Params]:
    return [Params(q, s, k) for q in QSTEPS for s in SCALES for k in KEYINTS]


def grid_fingerprint() -> str:
    spec = {
        "codec_version": CODEC_VERSION,
        "qsteps": list(QSTEPS),
        "scales": list(SCALES),
        "keyints": list(KEYINTS),
    }
    return hashlib.sha256(json.dumps(spec, sort_keys=True).encode()).hexdigest()


def _downsample(frames: np.ndarray, scale: int) -> np.ndarray:
    t, h, w, c = frames.shape
    if h % scale or w % scale:
        raise ValueError(f"frame size {w}x{h} not divisible by scale {scale}")
    if scale == 1:
        return frames.astype(np.int16)
    small = frames.reshape(t, h // scale, scale, w // scale, scale, c).mean(axis=(2, 4))
    return np.rint(small).astype(np.int16)


def _upscale(frames: np.ndarray, scale: int, shape: tuple) -> np.ndarray:
    if scale > 1:
        frames = np.repeat(np.repeat(frames, scale, axis=1), scale, axis=2)
    return np.clip(frames, 0, 255).astype(np.uint8).reshape(shape)


def encode_clip(frames: np.ndarray, params: Params) -> bytes:
    """Encode one clip (uint8 T,H,W,C) into a deterministic byte stream."""
    if frames.ndim != 4 or frames.shape[3] != 3:
        raise ValueError("expected frames with shape (T, H, W, 3)")
    t, h, w, _ = frames.shape
    if t < 1 or h * w > MAX_PIXELS:
        raise ValueError(f"unsupported clip shape {frames.shape}")
    small = _downsample(frames, params.scale)
    coeffs: list[np.ndarray] = []
    prev = np.zeros_like(small[0])
    for i in range(t):
        frame = small[i]
        if i % params.keyint == 0:
            quant = np.rint(frame / params.qstep).astype(np.int16)
            prev = quant.astype(np.int64) * params.qstep
        else:
            quant = np.rint((frame - prev) / params.qstep).astype(np.int16)
            prev = prev + quant.astype(np.int64) * params.qstep
        prev = prev.astype(np.int16)
        coeffs.append(quant)
    payload = zlib.compress(b"".join(c.tobytes() for c in coeffs), 9)
    header = {
        "v": CODEC_VERSION,
        "q": params.qstep,
        "s": params.scale,
        "k": params.keyint,
        "t": t,
        "h": h,
        "w": w,
    }
    hb = json.dumps(header, sort_keys=True).encode()
    return MAGIC + struct.pack("<I", len(hb)) + hb + payload


def decode_clip(blob: bytes) -> np.ndarray:
    """Decode back to uint8 frames at the original resolution."""
    if blob[:4] != MAGIC:
        raise ValueError("bad magic: not a ladderlab stream")
    (hlen,) = struct.unpack("<I", blob[4:8])
    header = json.loads(blob[8 : 8 + hlen])
    if header["v"] != CODEC_VERSION:
        raise ValueError(f"unsupported codec version {header['v']}")
    t, h, w = header["t"], header["h"], header["w"]
    s, q, k = header["s"], header["q"], header["k"]
    sh, sw = h // s, w // s
    raw = zlib.decompress(blob[8 + hlen :])
    expect = t * sh * sw * 3 * 2
    if len(raw) != expect:
        raise ValueError(f"payload size mismatch: got {len(raw)}, want {expect}")
    coeffs = np.frombuffer(raw, dtype=np.int16).reshape(t, sh, sw, 3).astype(np.int64)
    frames = np.empty((t, sh, sw, 3), dtype=np.int64)
    prev = np.zeros((sh, sw, 3), dtype=np.int64)
    for i in range(t):
        recon = coeffs[i] * q if i % k == 0 else prev + coeffs[i] * q
        frames[i] = recon
        prev = recon
    return _upscale(frames.astype(np.int16), s, (t, h, w, 3))


def stream_hash(blob: bytes) -> str:
    return hashlib.sha256(blob).hexdigest()
