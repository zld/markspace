#include "report.hpp"

#include <nlohmann/json.hpp>

#include <cstdio>
#include <filesystem>
#include <fstream>
#include <random>
#include <sstream>

namespace sp {

namespace {

using nlohmann::json;

std::string chain_to_string(const std::vector<ChainLink>& chain) {
    std::string out;
    for (const ChainLink& link : chain) {
        if (!out.empty()) out += " -> ";
        out += sanitize_for_output(link.name, 80);
        if (link.missing) out += " (missing)";
    }
    return out.empty() ? "(none)" : out;
}

std::string tag_summary_for_terminal(const InlineParse& parse) {
    return parse.counts.summary();
}

json chain_json(const std::vector<ChainLink>& chain) {
    json arr = json::array();
    for (const ChainLink& link : chain) {
        json item;
        item["name"] = link.name;
        item["line"] = link.line;
        item["missing"] = link.missing;
        arr.push_back(std::move(item));
    }
    return arr;
}

json adjustments_json(const std::vector<Adjustment>& adjustments) {
    json arr = json::array();
    for (const Adjustment& a : adjustments) {
        arr.push_back(json{{"field", a.field},
                           {"original", a.original},
                           {"fallback", a.fallback},
                           {"source", a.source}});
    }
    return arr;
}

json diagnostic_json(const Diagnostic& d) {
    return json{{"severity", severity_name(d.severity)},
                {"line", d.line},
                {"code", d.code},
                {"message", d.message},
                {"event_id", d.event_id}};
}

json render_json(const RenderResult& r, const Canvas& canvas) {
    json out;
    out["ok"] = r.ok;
    out["status"] = r.has_area ? "ok" : (r.empty_text ? "empty" : (r.ok ? "transparent" : "failed"));
    out["x"] = r.has_area ? json(r.x) : json(nullptr);
    out["y"] = r.has_area ? json(r.y) : json(nullptr);
    out["width"] = r.has_area ? json(r.width) : json(nullptr);
    out["height"] = r.has_area ? json(r.height) : json(nullptr);
    out["nontransparent_pixels"] = r.nontransparent_pixels;
    out["sha256"] = r.sha256;
    out["empty_text"] = r.empty_text;
    out["fully_transparent"] = r.fully_transparent;
    out["clipped"] = r.clipped;
    out["canvas"] = {{"width", canvas.width}, {"height", canvas.height}};
    if (!r.font_warnings.empty()) out["font_warnings"] = r.font_warnings;
    if (!r.error.empty()) out["error"] = r.error;
    return out;
}

}  // namespace


std::string build_json(const Report& report) {
    (void)report;
    return "{}";
}

void print_terminal_report(const Report& report) {
    (void)report;
}

bool write_json_report(const Report& report, const std::string& path, std::string& error) {
    (void)report;
    (void)path;
    error = "report writing is not implemented yet";
    return false;
}

}  // namespace sp
