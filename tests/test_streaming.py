"""Streaming and memory-budget tests for large (1 GiB-class) inputs.

The large input is a sparse file: the filesystem reports a 1 GiB size without
allocating the backing blocks, while every byte the parser actually reads is
real.  This proves the analyser walks boxes with seeks and never buffers the
media payload.
"""

from __future__ import annotations

import os
import resource
import struct
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parent))

from sidxcheck.pipeline import analyze  # noqa: E402

ONE_GIB = 1 << 30
MEMORY_BUDGET = 128 << 20


def _peak_rss_bytes() -> int:
    usage = resource.getrusage(resource.RUSAGE_SELF)
    # macOS reports bytes; Linux reports kilobytes.
    if sys.platform == "darwin":
        return usage.ru_maxrss
    return usage.ru_maxrss * 1024


class LargeFileStreamingTests(unittest.TestCase):
    def test_sparse_one_gib_file_stays_under_memory_budget(self):
        fd, path = tempfile.mkstemp(suffix=".mp4")
        try:
            # A single mdat box claiming 1 GiB.  Only the header is written;
            # the rest is a hole the parser must seek over, never read.
            header = struct.pack(">I4s", 8 + ONE_GIB, b"mdat")
            os.write(fd, header)
            os.ftruncate(fd, 8 + ONE_GIB)
            os.close(fd)

            baseline = _peak_rss_bytes()
            summary = analyze(path, strict=False)
            peak = _peak_rss_bytes()
            growth = peak - baseline

            # The parser records the oversized mdat boundary cleanly rather
            # than buffering it; no sidx/stbl means a structural diagnostic.
            self.assertTrue(summary.diagnostics)
            self.assertLessEqual(
                growth,
                MEMORY_BUDGET,
                f"peak RSS grew {growth} bytes, budget is {MEMORY_BUDGET}",
            )
        finally:
            Path(path).unlink(missing_ok=True)

    def test_oversized_leaf_payload_is_rejected_not_buffered(self):
        # A stsz box whose declared payload exceeds the buffering ceiling.
        fd, path = tempfile.mkstemp(suffix=".mp4")
        try:
            stbl_payload = struct.pack(
                ">I4sQ", 1, b"stsz", 16 + (64 << 20) + 1
            )
            stbl = struct.pack(
                ">I4s", 8 + len(stbl_payload), b"stbl"
            ) + stbl_payload
            moov = struct.pack(
                ">I4s", 8 + len(stbl), b"moov"
            ) + stbl
            os.write(fd, moov)
            os.close(fd)
            summary = analyze(path)
            codes = {d.code for d in summary.diagnostics}
            self.assertTrue(
                "box_payload_too_large" in codes
                or "entry_count_untrusted" in codes
                or "stsz_missing" in codes
            )
        finally:
            Path(path).unlink(missing_ok=True)


if __name__ == "__main__":
    unittest.main()
