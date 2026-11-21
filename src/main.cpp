#include <cstdio>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <string>

#include "layout_engine.hpp"
#include "pipeline.hpp"

using namespace asspipe;

namespace {

void print_usage() {
    std::cerr <<
        R"(usage:
  ass_pipeline demo --canvas WxH --output DIR --json FILE [--fonts-dir DIR]
  ass_pipeline render --input FILE --canvas WxH --output DIR --json FILE
                     [--fonts-dir DIR]

exit codes: 0 success, 2 invalid input, 3 render failure
)";
}

struct ParsedArgs {
    bool ok = false;
    PipelineOptions opts;
};

ParsedArgs parse_args(int argc, char** argv) {
    ParsedArgs args;
    if (argc < 2) return args;
    args.opts.command = argv[1];
    if (args.opts.command != "demo" && args.opts.command != "render")
        return args;

    for (int i = 2; i < argc; ++i) {
        std::string flag = argv[i];
        auto need_value = [&](std::string& target) {
            if (i + 1 >= argc) return false;
            target = argv[++i];
            return true;
        };
        if (flag == "--canvas") {
            if (!need_value(flag) ||
                !parse_canvas(flag, args.opts.canvas))
                return args;
        } else if (flag == "--output") {
            if (!need_value(args.opts.output_dir)) return args;
        } else if (flag == "--json") {
            if (!need_value(args.opts.json_path)) return args;
        } else if (flag == "--input") {
            if (!need_value(args.opts.input_path)) return args;
        } else if (flag == "--fonts-dir") {
            if (!need_value(args.opts.fonts_dir)) return args;
        } else {
            return args;
        }
    }
    if (args.opts.command == "render" && args.opts.input_path.empty())
        return args;
    if (args.opts.output_dir.empty() || args.opts.json_path.empty())
        return args;
    args.ok = true;
    return args;
}

} // namespace

int main(int argc, char** argv) {
    ParsedArgs args = parse_args(argc, argv);
    if (!args.ok) {
        print_usage();
        return EXIT_INVALID_INPUT;
    }

    PipelineResult result = run_pipeline(args.opts);

    std::cout << "styles=" << result.styles
              << " events=" << result.events
              << " inheritance_errors=" << result.inheritance_errors
              << " inline_tags=" << result.inline_tags
              << " unknown_tags=" << result.unknown_tags
              << " rendered=" << result.rendered << "\n";
    for (const auto& rec : result.records) {
        std::cout << "event_id=" << rec.event_id << " x=" << rec.x
                  << " y=" << rec.y << " width=" << rec.width
                  << " height=" << rec.height << " sha256=" << rec.sha256
                  << "\n";
    }
    for (const auto& d : result.diagnostics) {
        std::cerr << (d.level == DiagLevel::Error ? "error" : "warning")
                  << " [" << d.category() << "] line=" << d.line
                  << " event=" << d.event_index << " " << d.message << "\n";
    }

    if (!args.opts.json_path.empty()) {
        std::error_code ec;
        std::filesystem::create_directories(
            std::filesystem::path(args.opts.json_path).parent_path(), ec);
        std::ofstream jf(args.opts.json_path, std::ios::binary);
        if (!jf) {
            std::cerr << "error [path_rejected] cannot write JSON manifest: "
                      << args.opts.json_path << "\n";
            return EXIT_INVALID_INPUT;
        }
        jf << render_json(result);
    }
    return result.exit_code;
}
