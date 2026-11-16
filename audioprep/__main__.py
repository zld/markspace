"""Entry point for ``python -m audioprep``.

Delegates to :func:`audioprep.cli.main`; the process exit code follows the
CLI contract (0 ok, 1 file failure, 2 usage/configuration error, 3 verify
mismatch).
"""

from audioprep.cli import main

if __name__ == "__main__":
    raise SystemExit(main())
