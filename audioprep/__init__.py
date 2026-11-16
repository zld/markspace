"""audioprep: local, reproducible batch audio preparation.

The package turns heterogeneous delivery audio (mixed sample rates, channel
layouts and loudness) into engine-ready PCM_24 material with an offline,
re-computable JSON report. Resampling and loudness measurement are implemented
from scratch in :mod:`audioprep.resampler` and :mod:`audioprep.loudness`.
"""

__version__ = "0.1.0"

__all__ = ["__version__"]
