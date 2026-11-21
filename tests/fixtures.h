#pragma once

#include <string>

// Deterministic demo manifest used by `subtitle-metrics --make-fixture` and by
// tests/run.sh. It exercises plain text, combining marks, an emoji/ZWJ cluster,
// explicit newlines, whitespace wrapping, a long unbreakable word, max_lines
// overflows, a timing marker and exactly one missing glyph. The font path
// honors SUBTITLE_METRICS_FONT and otherwise names the macOS Arial build.
std::string make_fixture_json();
