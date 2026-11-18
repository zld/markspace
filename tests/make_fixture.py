"""Generate the multi-track MKV fixture used by the test suite.

Usage: python -m tests.make_fixture [output_path]
Defaults to tests/fixtures/demo.mkv next to this script.
"""
from __future__ import annotations

import os
import sys

from mkvmp4.fixture import build_fixture

DEFAULT_OUTPUT = os.path.join(os.path.dirname(__file__), "fixtures", "demo.mkv")


def main(argv=None) -> str:
    argv = argv if argv is not None else sys.argv[1:]
    output = argv[0] if argv else DEFAULT_OUTPUT
    os.makedirs(os.path.dirname(os.path.abspath(output)), exist_ok=True)
    data = build_fixture()
    with open(output, "wb") as fh:
        fh.write(data)
    print(f"wrote {output} ({len(data)} bytes)")
    return output


if __name__ == "__main__":
    main()
