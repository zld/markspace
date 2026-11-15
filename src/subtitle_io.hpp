#pragma once

#include <string>
#include <vector>

#include "config.hpp"

namespace sm {

struct SubtitleInput {
    std::string id;
    std::string text;
    std::string language;  // optional; empty means absent
};

// Reads and validates a subtitle JSON document:
//   { "subtitles": [ {"id": "...", "text": "...", "lang"?: "..."} ] }
// On failure returns false with an ErrorKind::Config message that names
// the offending entry position (1-based) and field. Enforces
// kMaxSubtitles and kMaxSubtitleBytes from config.hpp.
bool loadSubtitles(const std::string& path,
                   std::vector<SubtitleInput>& out, Error& error);

// Generates the deterministic demo fixture (~20 entries) covering CJK,
// combining marks, emoji/ZWJ, fullwidth punctuation, hard newlines,
// empty text and an unbreakable long identifier. Writing failures are
// reported as ErrorKind::Output.
bool writeFixture(const std::string& path, Error& error);

}  // namespace sm
