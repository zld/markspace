"""mediapack: MP4 fragment index rebuild and sample-table validation.

Handles only the baseline MP4 subset produced by this project's fixture
generator (ftyp/sidx/moov/mdat with stts/stsc/stsz/stco/co64/stss inside
stbl). It is not a general-purpose MP4 decoder.
"""

__version__ = "0.1.0"
