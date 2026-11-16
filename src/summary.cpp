#include "summary.hpp"

namespace subaudit {
namespace {

std::string json_escape(const std::string& s) {
    std::string o;
    for (const char c : s) {
        switch (c) {
            case '"': o += "\\\""; break;
            case '\\': o += "\\\\"; break;
            default:
                if (static_cast<unsigned char>(c) < 0x20) {
                    char b[8];
                    std::snprintf(b, sizeof b, "\\u%04x", c);
                    o += b;
                } else {
                    o += c;
                }
        }
    }
    return o;
}

std::string hex16(std::uint16_t v) {
    char b[8];
    std::snprintf(b, sizeof b, "%04x", v);
    return b;
}

std::string counts_json(const std::map<std::string, int>& counts) {
    std::string j = "{";
    bool first = true;
    for (const auto& kv : counts) {
        j += (first ? "" : ", ");
        j += "\"" + json_escape(kv.first) + "\": " + std::to_string(kv.second);
        first = false;
    }
    return j + "}";
}

}  // namespace

std::string report_to_json(const AuditReport& r) {
    std::string j = "{\n";
    j += "  \"canvas\": {\"w\": " + std::to_string(r.canvas_w) +
         ", \"h\": " + std::to_string(r.canvas_h) + "},\n";
    j += "  \"jobs\": " + std::to_string(r.jobs) + ",\n";
    j += "  \"style_chains\": [";
    for (std::size_t i = 0; i < r.styles.size(); ++i) {
        const ResolvedStyle& st = r.styles[i];
        j += i ? ",\n    " : "\n    ";
        j += "{\"style\": \"" + json_escape(st.name) + "\", \"chain\": [";
        for (std::size_t k = 0; k < st.chain.size(); ++k) {
            if (k) j += ", ";
            j += "\"" + json_escape(st.chain[k]) + "\"";
        }
        j += "]}";
    }
    j += "\n  ],\n";
    j += "  \"recognized_tags\": " + std::to_string(r.tags.recognized_total) + ",\n";
    j += "  \"recognized_tag_counts\": " + counts_json(r.tags.recognized_counts) + ",\n";
    j += "  \"unknown_tags\": " + std::to_string(r.tags.unknown_total) + ",\n";
    j += "  \"unknown_tag_counts\": " + counts_json(r.tags.unknown_counts) + ",\n";
    j += "  \"malformed_tags\": " + std::to_string(r.tags.malformed) + ",\n";
    j += "  \"event_regions\": " + std::to_string(r.regions.size()) + ",\n";
    j += "  \"events\": [";
    for (std::size_t i = 0; i < r.regions.size(); ++i) {
        const RenderedRegion& g = r.regions[i];
        j += i ? ",\n    " : "\n    ";
        j += "{\"event_id\": " + std::to_string(g.event_id) +
             ", \"x\": " + std::to_string(g.x) + ", \"y\": " + std::to_string(g.y) +
             ", \"w\": " + std::to_string(g.w) + ", \"h\": " + std::to_string(g.h) +
             ", \"pixel_digest\": \"" + hex16(g.pixel_digest) + "\"}";
    }
    j += "\n  ]\n}\n";
    return j;
}

void print_report(const AuditReport& r, std::FILE* out) {
    std::fprintf(out, "style_chains: %zu\n", r.styles.size());
    for (const ResolvedStyle& st : r.styles) {
        std::string chain;
        for (std::size_t k = 0; k < st.chain.size(); ++k) {
            if (k) chain += " -> ";
            chain += st.chain[k];
        }
        std::fprintf(out, "  %s\n", chain.c_str());
    }
    std::fprintf(out, "recognized_tags: %d\n", r.tags.recognized_total);
    for (const auto& kv : r.tags.recognized_counts)
        std::fprintf(out, "  %s: %d\n", kv.first.c_str(), kv.second);
    std::fprintf(out, "unknown_tags: %d\n", r.tags.unknown_total);
    for (const auto& kv : r.tags.unknown_counts)
        std::fprintf(out, "  %s: %d\n", kv.first.c_str(), kv.second);
    std::fprintf(out, "malformed_tags: %d\n", r.tags.malformed);
    std::fprintf(out, "event_regions: %zu\n", r.regions.size());
    for (const RenderedRegion& g : r.regions)
        std::fprintf(out, "event %d: x=%d y=%d w=%d h=%d pixel_digest=%04x\n",
                     g.event_id, g.x, g.y, g.w, g.h, g.pixel_digest);
}

}  // namespace subaudit
