#pragma once

// SRT parsing: BOM, CRLF, multi-line cue text, timestamp validation.
// Errors are recorded with 1-based line numbers; valid cues survive.

#include <string>

#include "subtitle/model.hpp"

namespace subtitle {

ParseResult parse_srt(const std::string& bytes);

}  // namespace subtitle
