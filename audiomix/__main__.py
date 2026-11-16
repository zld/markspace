"""Allow `python -m audiomix ...` by delegating to the CLI entry point."""
from .cli import main

if __name__ == "__main__":
    raise SystemExit(main())
