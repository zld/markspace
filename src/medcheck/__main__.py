"""Allow `python -m medcheck ...` by delegating to the CLI."""

from .cli import entrypoint

entrypoint()
