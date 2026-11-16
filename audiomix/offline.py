"""Offline executor: renders the whole mix graph in a single pull."""
from __future__ import annotations


def render_offline(graph):
    """Render the entire graph at once and return the (frames, 2) mix."""
    return graph.pull(graph.total_frames)
