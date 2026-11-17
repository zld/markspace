"""trackmix: deterministic local multi-rate audio mixing.

The package mixes dialog, music and ambient tracks described by a JSON
manifest into a master timeline.  Offline (whole-timeline) and streaming
(arbitrary block length) entry points evaluate the *same* mixing graph so
the two paths are bit-comparable up to floating-point associativity.
"""

__version__ = "0.1.0"
