#pragma once
#include <string>

#include "ass_model.hpp"

namespace assaudit {

// Parse a complete UTF-8 ASS document. Never throws on malformed input;
// every problem becomes a Diagnostic carrying its 1-based source line.
// Structural problems are flagged fatal and make strict mode fail.
bool parseAss(const std::string& content, const std::string& sourceLabel,
              AssDocument& doc);

}  // namespace assaudit
