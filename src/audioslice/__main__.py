"""Allow ``python -m audioslice``."""
import sys

from .cli import main

sys.exit(main())
