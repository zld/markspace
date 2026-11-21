"""Module entry point: ``python -m audiomix``."""

import sys

from .cli import main

if __name__ == "__main__":
    sys.exit(main())
