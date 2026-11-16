"""Module entry point so ``python -m mkvmp4`` runs the CLI."""

from .cli import main

if __name__ == "__main__":
    raise SystemExit(main())
