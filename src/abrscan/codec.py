"""Deterministic block-transform codec proxy.

This is intentionally **not** a production video codec.  Each frame is split
into square blocks, transformed with an orthonormal 2D DCT, quantised, packed
as little-endian int16 coefficients and compressed with zlib.  The resulting
byte count is a reproducible stand-in for encoded size when comparing
parameter combinations.

The transport format is::

    MAGIC (4) | version (1) | width (u16) | height (u16) | block (u8)
    | quant (u16) | frame_count (u32) | per frame: length (u32) + zlib data

Decoding validates every length, dimension and CRC so corrupted output never
reaches the metrics layer.
"""

from __future__ import annotations

import hashlib
import struct
import zlib
from dataclasses import dataclass

import numpy as np

MAGIC = b"ABR1"
VERSION = 1
_HEADER = struct.Struct("<4sBHHBHI")
_FRAME_LEN = struct.Struct("<I")
_FRAME_CRC = struct.Struct("<I")

MAX_FRAMES = 240
MAX_DIMENSION = 320


class CodecError(ValueError):
    """Raised when input cannot be encoded or output fails validation."""


@dataclass(frozen=True)
class EncodedClip:
    """Compressed clip payload plus the settings that produced it."""

    payload: bytes
    width: int
    height: int
    block_size: int
    quant_step: int
    frame_count: int
    fps: int

    @property
    def encoded_bytes(self) -> int:
        return len(self.payload)


def _dct_matrix(size: int) -> np.ndarray:
    """Orthonormal DCT-II matrix of shape (size, size), float64."""
    n = np.arange(size, dtype=np.float64)
    k = n[:, None]
    matrix = np.cos(np.pi * (2.0 * n[None, :] + 1.0) * k / (2.0 * size))
    matrix *= np.sqrt(2.0 / size)
    matrix[0, :] *= np.sqrt(0.5)
    return matrix


def _validate_settings(quant_step: int, block_size: int) -> None:
    if not (1 <= quant_step <= 255):
        raise CodecError(f"quantisation step {quant_step} is out of range (1..255)")
    if block_size not in (4, 8, 16):
        raise CodecError(f"unsupported block size {block_size}")


def _encode_frame(frame: np.ndarray, matrix: np.ndarray, quant: int) -> bytes:
    height, width = frame.shape
    block = matrix.shape[0]
    # (blocks_y, block, blocks_x, block) -> (blocks_y, blocks_x, block, block)
    tiles = frame.reshape(height // block, block, width // block, block)
    tiles = tiles.transpose(0, 2, 1, 3).astype(np.float64)
    coefficients = matrix @ tiles @ matrix.T
    quantised = np.rint(coefficients / float(quant))
    limit = np.iinfo(np.int16).max
    if np.any(np.abs(quantised) > limit):
        raise CodecError("quantised coefficient exceeds int16 range")
    packed = quantised.astype("<i2").tobytes()
    return zlib.compress(packed, level=6)


def encode_clip(
    frames: np.ndarray, quant_step: int, block_size: int, fps: int
) -> EncodedClip:
    """Encode a (N, H, W) uint8 clip with the block-transform proxy."""
    _validate_settings(quant_step, block_size)
    if not isinstance(frames, np.ndarray) or frames.dtype != np.uint8:
        raise CodecError("frames must be a uint8 numpy array")
    if frames.ndim != 3:
        raise CodecError("frames must have shape (count, height, width)")
    count, height, width = frames.shape
    if count < 1 or count > MAX_FRAMES:
        raise CodecError(f"frame count must be in 1..{MAX_FRAMES}")
    if not (1 <= height <= MAX_DIMENSION and 1 <= width <= MAX_DIMENSION):
        raise CodecError("frame dimensions are out of range")
    if height % block_size or width % block_size:
        raise CodecError(
            f"{width}x{height} is not divisible by block size {block_size}"
        )
    if fps < 1 or fps > 120:
        raise CodecError("fps is out of range")

    matrix = _dct_matrix(block_size)
    header = _HEADER.pack(
        MAGIC, VERSION, width, height, block_size, quant_step, count
    )
    chunks = [header]
    for frame in frames:
        raw = _encode_frame(frame, matrix, quant_step)
        crc = zlib.crc32(raw) & 0xFFFFFFFF
        chunks.append(_FRAME_CRC.pack(crc))
        chunks.append(_FRAME_LEN.pack(len(raw)))
        chunks.append(raw)
    return EncodedClip(
        payload=b"".join(chunks),
        width=width,
        height=height,
        block_size=block_size,
        quant_step=quant_step,
        frame_count=count,
        fps=fps,
    )


def decode_clip(clip: EncodedClip | bytes) -> np.ndarray:
    """Decode and validate a payload, returning a uint8 (N, H, W) array."""
    payload = clip.payload if isinstance(clip, EncodedClip) else bytes(clip)
    if len(payload) < _HEADER.size:
        raise CodecError("payload is shorter than the header")
    magic, version, width, height, block, quant, count = _HEADER.unpack_from(
        payload, 0
    )
    if magic != MAGIC:
        raise CodecError("payload magic mismatch")
    if version != VERSION:
        raise CodecError(f"unsupported payload version {version}")
    _validate_settings(quant, block)
    if not (1 <= count <= MAX_FRAMES):
        raise CodecError("decoded frame count is out of range")
    if not (1 <= width <= MAX_DIMENSION and 1 <= height <= MAX_DIMENSION):
        raise CodecError("decoded dimensions are out of range")
    if width % block or height % block:
        raise CodecError("decoded dimensions do not match the block size")

    matrix = _dct_matrix(block)
    output = np.empty((count, height, width), dtype=np.uint8)
    offset = _HEADER.size
    for index in range(count):
        if offset + _FRAME_CRC.size + _FRAME_LEN.size > len(payload):
            raise CodecError(f"payload truncated before frame {index}")
        (crc,) = _FRAME_CRC.unpack_from(payload, offset)
        offset += _FRAME_CRC.size
        (length,) = _FRAME_LEN.unpack_from(payload, offset)
        offset += _FRAME_LEN.size
        if length > len(payload) - offset:
            raise CodecError(f"frame {index} length exceeds payload")
        raw = payload[offset : offset + length]
        offset += length
        if (zlib.crc32(raw) & 0xFFFFFFFF) != crc:
            raise CodecError(f"frame {index} checksum mismatch")
        try:
            decoded_raw = zlib.decompress(raw)
        except zlib.error as exc:
            raise CodecError(f"frame {index} decompression failed: {exc}") from exc
        expected = width * height * 2
        if len(decoded_raw) != expected:
            raise CodecError(
                f"frame {index} decoded to {len(decoded_raw)} bytes, expected {expected}"
            )
        coefficients = np.frombuffer(decoded_raw, dtype="<i2").astype(np.float64)
        coefficients = coefficients.reshape(
            height // block, width // block, block, block
        )
        coefficients *= float(quant)
        tiles = matrix.T @ coefficients @ matrix
        frame = tiles.transpose(0, 2, 1, 3).reshape(height, width)
        output[index] = np.clip(np.rint(frame), 0, 255).astype(np.uint8)

    if offset != len(payload):
        raise CodecError("payload has trailing bytes")
    return output


def payload_digest(payload: bytes) -> str:
    """Stable digest used when recording measurement provenance."""
    return hashlib.sha256(payload).hexdigest()
