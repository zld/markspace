// subtitle-audit: audit summary (JSON document + terminal report).
#pragma once

#include <cstdio>
#include <string>
#include <vector>

#include "libass_renderer.hpp"
#include "style_graph.hpp"
#include "tag_machine.hpp"

namespace subaudit {

struct AuditReport {
    int canvas_w = 0;
    int canvas_h = 0;
    int jobs = 1;
    std::vector<ResolvedStyle> styles;
    TagStats tags;
    std::vector<RenderedRegion> regions;
};

// Serializes the report as a stable, recomputable JSON document.
std::string report_to_json(const AuditReport& r);

// Prints the human-readable terminal report (same facts as the JSON).
void print_report(const AuditReport& r, std::FILE* out);

}  // namespace subaudit
