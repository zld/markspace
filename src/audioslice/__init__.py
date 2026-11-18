"""audioslice: extract dialogue and sound events from long PCM recordings.

Pipeline: sample generator / PCM input -> batch scheduler -> detector +
segment shaping -> SQLite store -> CLI summary. Dependencies point inward
(cli/batch -> detector/segments/store); nothing here talks to the network.
"""

__version__ = "0.1.0"
