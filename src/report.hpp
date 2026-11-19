#pragma once

#include <string>
#include <vector>

#include "ass_document.hpp"
#include "inline_tags.hpp"
#include "layout_probe.hpp"
#include "style_resolver.hpp"

namespace sp {

struct Canvas {
    int width = 1920;
    int height = 1080;
};

struct EventReport {
    int event_id = 0;
    int line = 0;
    int layer = 0;
    long long start_ms = 0;
    long long end_ms = 0;
    std::string style_requested;
    std::vector<ChainLink> style_chain;
    std::vector<Adjustment> adjustments;
    std::vector<std::string> cleared_fields;
    InlineParse inline_result;
    std::string text;          // canonical text handed to libass
    std::string raw_text;
    RenderResult render;
    std::vector<Diagnostic> diagnostics;
    bool skipped = false;
    std::string skip_reason;
    std::string status;        // "rendered", "empty", "transparent", "font-fallback", "failed"
};

struct RunSummary {
    int events = 0;
    int styles = 0;
    int rendered = 0;
    int failed = 0;
    int empty = 0;
    int transparent = 0;
    int clipped = 0;
    long long nontransparent_pixels = 0;
    std::string combined_sha256;
};

struct Report {
    std::string input_path;
    Canvas canvas;
    std::string font_dir;
    std::string libass_version;
    std::vector<EventReport> events;
    std::vector<Diagnostic> document_diagnostics;  // parse + style graph
    RunSummary summary;
};

void print_terminal_report(const Report& report);
bool write_json_report(const Report& report, const std::string& path, std::string& error);
std::string build_json(const Report& report);

}  // namespace sp
