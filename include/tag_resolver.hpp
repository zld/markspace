#pragma once
#include "ass_model.hpp"

namespace assaudit {

// Scan one event Text for {\...} groups. Scope is per call (per event):
// every invocation starts from a clean state. Unknown/malformed tags are
// counted and skipped, unclosed groups are consumed to end-of-line.
TagReport resolveTags(const std::string& text);

}  // namespace assaudit
