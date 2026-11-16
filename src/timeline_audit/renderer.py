"""Frame rendering scheduler.

render_frame() composites one frame purely in memory. render_all()
adds bounded parallelism: frames may complete out of order but are
always delivered to the callback in ascending frame order. Any render
failure cancels the remaining futures and propagates as RenderError.
"""

from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor, as_completed
from fractions import Fraction

import numpy as np

from .compositor import composite_region, scale_layer
from .curves import evaluate
from .model import Timeline


class RenderError(Exception):
    """Raised when frame rendering fails."""


def render_frame(timeline: Timeline, assets: dict[str, np.ndarray], index: int) -> bytes:
    """Composite one frame and return canonical RGBA bytes (row-major)."""
    canvas = np.zeros((timeline.height, timeline.width, 4), dtype=np.uint8)
    t = Fraction(index) / timeline.fps
    for layer in timeline.layers:
        if not (layer.start <= t < layer.end):
            continue
        src = assets.get(layer.asset)
        if src is None:
            continue  # missing layer: skip rather than fail the frame
        opacity = evaluate(layer.opacity, index)
        scaled = scale_layer(src, layer.scale)
        composite_region(
            canvas, scaled, round(layer.x), round(layer.y), opacity, layer.blend
        )
    return canvas.tobytes()


def render_all(
    timeline: Timeline,
    assets: dict[str, np.ndarray],
    workers: int,
    on_frame,
) -> int:
    """Render every frame, invoking on_frame(index, rgba_bytes) in order.

    ``workers`` bounds parallelism; workers <= 1 renders inline. Returns
    the number of frames rendered.
    """
    n = timeline.frames
    if n == 0:
        return 0
    if workers <= 1:
        for i in range(n):
            try:
                data = render_frame(timeline, assets, i)
            except Exception as exc:
                raise RenderError(f"frame {i}: {exc}") from exc
            on_frame(i, data)
        return n

    results: dict[int, bytes] = {}
    with ThreadPoolExecutor(max_workers=workers) as pool:
        futures = {
            pool.submit(render_frame, timeline, assets, i): i for i in range(n)
        }
        try:
            for fut in as_completed(futures):
                i = futures[fut]
                try:
                    results[i] = fut.result()
                except Exception as exc:
                    raise RenderError(f"frame {i}: {exc}") from exc
        except BaseException:
            for fut in futures:
                fut.cancel()
            raise
    for i in range(n):
        on_frame(i, results[i])
    return n
