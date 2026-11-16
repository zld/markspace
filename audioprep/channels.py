"""Fixed, frame-accurate channel layout conversion rules.

Only mono (1) and interleaved stereo (2) are supported. The mapping rules are
deterministic and apply independently per frame:

* ``keep``   -- no conversion, output layout matches the input;
* ``mono``   -- stereo input is downmixed per frame as ``(L + R) / 2``,
  mono passes through;
* ``stereo`` -- mono input is duplicated to identical L/R frames, stereo
  passes through.

No filtering, delay or cross-frame state is involved.
"""

from __future__ import annotations

import numpy as np

MONO = 1
STEREO = 2
SUPPORTED = (MONO, STEREO)
MODES = ("keep", "mono", "stereo")


class ChannelError(ValueError):
    """Raised for unsupported channel counts or conversion modes."""


def output_channels(input_channels: int, mode: str) -> int:
    """Resolve the output channel count for an input/layout pair."""
    if input_channels not in SUPPORTED:
        raise ChannelError(f"unsupported channel count: {input_channels}")
    if mode not in MODES:
        raise ChannelError(f"unknown channel mode: {mode!r}")
    if mode == "keep":
        return input_channels
    return MONO if mode == "mono" else STEREO


def map_channels(block: np.ndarray, input_channels: int, mode: str) -> np.ndarray:
    """Convert one ``(frames, channels)`` block under the fixed rules."""
    block = np.asarray(block, dtype=np.float64)
    if block.ndim != 2 or block.shape[1] != input_channels:
        raise ChannelError(
            f"block shape {block.shape} does not declare {input_channels} channels"
        )
    target = output_channels(input_channels, mode)
    if target == input_channels:
        return block
    if input_channels == STEREO and target == MONO:
        # Equal-weight downmix, one output frame per input frame.
        return np.mean(block, axis=1, keepdims=True)
    if input_channels == MONO and target == STEREO:
        # Identical L and R, one output frame per input frame.
        return np.repeat(block, 2, axis=1)
    raise ChannelError(
        f"cannot map {input_channels} channels to {target} via {mode!r}"
    )
