"""Build the valid and corrupt sample files used by the test-suite.

Runnable directly:  python tests/build_fixtures.py <outdir>
"""
from __future__ import annotations

import sys
from pathlib import Path

try:
    from mediapack import fixture
except ImportError:  # allow running from a source checkout without install
    sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
    from mediapack import fixture


def build_all(outdir) -> dict[str, Path]:
    out = Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    built = {
        "valid": ("valid.mp4", {}),
        "offset-drift": ("offset-drift.mp4", {"stbl_offset_delta": 4}),
        "truncated": ("truncated.mp4", {"truncate": 96}),
        "no-sidx": ("no-sidx.mp4", {"drop_sidx": True}),
        "co64": ("co64.mp4", {"use_co64": True}),
        "stsc-mismatch": ("stsc-mismatch.mp4", {"stsc_override": [(1, 3, 1)]}),
        "tampered": ("tampered.mp4", {"tamper_sample": 3}),
    }
    paths = {}
    for name, (filename, kwargs) in built.items():
        path = out / filename
        fixture.build_fixture(path, **kwargs)
        paths[name] = path
    return paths


if __name__ == "__main__":
    dest = sys.argv[1] if len(sys.argv) > 1 else ".fixtures"
    for name, path in build_all(dest).items():
        print(f"{name}: {path}")
