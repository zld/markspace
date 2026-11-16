// subtitle-audit: style inheritance resolution (Parent extension, "~").
#pragma once

#include <map>
#include <string>
#include <vector>

#include "document.hpp"

namespace subaudit {

struct ResolvedStyle {
    std::string name;
    std::map<std::string, std::string> fields;  // fully resolved, "~" expanded
    std::vector<std::string> chain;             // root .. leaf style names
};

// Resolves the Parent extension and "~" inherit markers for every style.
// Fails with a line-numbered error on duplicate names, unknown parents,
// inheritance cycles and "~" fields no parent defines.
bool resolve_styles(const Document& doc, std::vector<ResolvedStyle>& out,
                    std::string& err);

}  // namespace subaudit
