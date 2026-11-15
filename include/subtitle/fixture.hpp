#pragma once

// Deterministic sample SRT generator: mixed CJK/Latin, combining marks,
// explicit newlines, URLs and unbreakable long tokens. No external data.

#include <cstddef>
#include <string>

namespace subtitle {

// Produces `count` cues. Every 16th cue (i % 16 == 15) carries a single
// unbreakable long token, so count=128 yields exactly 8 width-overflow
// entries at font-size 32 / max-width 640.
std::string generate_fixture(std::size_t count);

}  // namespace subtitle
