#pragma once
#include <string>

#include "ass_model.hpp"

namespace assaudit {

struct ResolveStats {
  int inheritance_edges = 0;
  int inheritance_errors = 0;  // missing parent, cycle, duplicate name
};

// Build each style's inheritance chain and fold fields along it.
// Chains are child-first; the first style in the chain wins every field.
// Blocked styles keep diagnostics and are excluded from rendering later.
ResolveStats resolveStyles(AssDocument& doc);

// ASS V4+ defaults, exposed for the renderer's synthesized Style lines.
std::map<std::string, std::string> styleDefaults();

}  // namespace assaudit
