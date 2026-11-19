"""Bootstrap that puts the src/ layout on sys.path without installing the package.

The acceptance workflow runs ``python -m audioslice.cli`` and
``python -m unittest discover -s tests`` straight from a checkout, without an
editable install. This file is imported by every test module; the same trick
for CLI invocation lives in /sitecustomize.py at the repository root.
"""

import sys
from pathlib import Path

SRC = Path(__file__).resolve().parent.parent / "src"
if str(SRC) not in sys.path:
    sys.path.insert(0, str(SRC))
