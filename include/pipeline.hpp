#pragma once
//
// pipeline.hpp — End-to-end orchestration shared by the CLI and tests.
//
#include <string>
#include <vector>

#include "layout_engine.hpp"

namespace asspipe {

// Exit codes mandated by the specification.
constexpr int EXIT_OK = 0;
constexpr int EXIT_INVALID_INPUT = 2;
constexpr int EXIT_RENDER_FAILURE = 3;

struct PipelineOptions {
    std::string command;             // "demo" | "render"
    std::string input_path;          // render: source ASS file
    std::string output_dir = "out";
    std::string json_path;           // summary manifest path
    Canvas canvas{1280, 720};
    std::string fonts_dir;
};

struct EventRecord {
    int event_id = 0;
    int x = 0;
    int y = 0;
    int width = 0;
    int height = 0;
    std::string sha256;
    std::string png_path;
};

struct PipelineResult {
    int exit_code = EXIT_OK;
    int styles = 0;
    int events = 0;
    int inheritance_errors = 0;
    int inline_tags = 0;
    int unknown_tags = 0;
    int rendered = 0;
    std::vector<EventRecord> records;
    std::vector<Diagnostic> diagnostics;
};

// Run the full pipeline. Never throws on malformed input.
PipelineResult run_pipeline(const PipelineOptions& opts);

// Serialize the result as a UTF-8 JSON document.
std::string render_json(const PipelineResult& result);

} // namespace asspipe
