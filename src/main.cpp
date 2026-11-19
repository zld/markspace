#include <ass/ass.h>

#include <algorithm>
#include <atomic>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <map>
#include <optional>
#include <string>
#include <thread>
#include <vector>

#include "ass_document.hpp"
#include "inline_tags.hpp"
#include "layout_probe.hpp"
#include "report.hpp"
#include "style_resolver.hpp"

namespace fs = std::filesystem;

namespace {

constexpr int kExitOk = 0;
constexpr int kExitInput = 1;
constexpr int kExitPartialRender = 2;
constexpr int kExitReportWrite = 3;

struct Options {
    std::string input;
    std::string json_path;
    std::string font_dir;
    sp::Canvas canvas;
    int jobs = 1;
    bool strict = false;
    bool font_dir_given = false;
};

void print_usage() {
    std::printf(
        "usage: subtitle_probe --input FILE [--canvas WxH] [--font-dir DIR] [--json FILE]\n"
        "                      [--jobs N] [--strict]\n"
        "\n"
        "  --input FILE     ASS script to inspect (required)\n"
        "  --canvas WxH     off-screen frame size (default 1920x1080)\n"
        "  --font-dir DIR   directory scanned for fonts (libass provider: none)\n"
        "  --json FILE      write the full report as JSON (atomic replace)\n"
        "  --jobs N         render worker threads (output order is preserved)\n"
        "  --strict         treat input and style diagnostics as fatal errors\n"
        "\n"
        "exit codes: 0 all ok, 1 input/argument error, 2 some events failed to render,\n"
        "            3 report could not be written\n");
}

std::string sanitize_path(const std::string& path) { return sp::sanitize_for_output(path, 300); }

// libass recursively opens every entry under the font directory. A FIFO or
// device node would block that scan indefinitely, so reject such trees up
// front instead of hanging the whole run.
bool validate_font_dir_contents(const std::string& path, std::string& error) {
    std::error_code ec;
    size_t entries = 0;
    fs::recursive_directory_iterator it(path, fs::directory_options::skip_permission_denied, ec);
    if (ec) {
        error = "cannot scan --font-dir: " + ec.message();
        return false;
    }
    for (fs::recursive_directory_iterator end; it != end; it.increment(ec)) {
        if (ec) break;
        if (++entries > 20000) {
            error = "--font-dir contains more than 20000 entries; point it at a font directory";
            return false;
        }
        const fs::file_status link_status = it->symlink_status(ec);
        if (ec) { ec.clear(); continue; }
        if (fs::is_directory(link_status) || fs::is_regular_file(link_status)) continue;
        if (fs::is_symlink(link_status)) {
            const fs::file_status target = fs::status(it->path(), ec);
            if (!ec && fs::is_regular_file(target)) continue;
        }
        error = "--font-dir contains a non-regular file (FIFO, socket or device) at '" +
                sanitize_path(it->path().string()) + "'";
        return false;
    }
    return true;
}

bool parse_canvas(const std::string& text, sp::Canvas& out, std::string& error) {
    size_t x = text.find('x');
    if (x == std::string::npos) x = text.find('X');
    if (x == std::string::npos) {
        error = "--canvas expects WIDTHxHEIGHT, got '" + sanitize_path(text) + "'";
        return false;
    }
    try {
        size_t used = 0;
        long w = std::stol(text.substr(0, x), &used);
        if (used != x) throw std::invalid_argument("width");
        size_t used2 = 0;
        long h = std::stol(text.substr(x + 1), &used2);
        if (used2 != text.size() - x - 1) throw std::invalid_argument("height");
        if (w <= 0 || h <= 0 || w > 32768 || h > 32768) {
            error = "--canvas dimensions out of range: " + sanitize_path(text);
            return false;
        }
        out.width = static_cast<int>(w);
        out.height = static_cast<int>(h);
        return true;
    } catch (...) {
        error = "--canvas expects WIDTHxHEIGHT, got '" + sanitize_path(text) + "'";
        return false;
    }
}

bool parse_jobs(const std::string& text, int& out, std::string& error) {
    try {
        size_t used = 0;
        long n = std::stol(text, &used);
        if (used != text.size() || n < 1 || n > 256) throw std::invalid_argument("jobs");
        out = static_cast<int>(n);
        return true;
    } catch (...) {
        error = "--jobs expects an integer in [1,256], got '" + sanitize_path(text) + "'";
        return false;
    }
}

bool parse_args(int argc, char** argv, Options& options, std::string& error) {
    for (int i = 1; i < argc; ++i) {
        std::string arg = argv[i];
        auto next = [&](const char* name) -> std::optional<std::string> {
            if (i + 1 >= argc) {
                error = std::string("missing value for ") + name;
                return std::nullopt;
            }
            return std::string(argv[++i]);
        };
        if (arg == "--input" || arg == "-i") {
            auto v = next("--input"); if (!v) return false; options.input = *v;
        } else if (arg == "--json") {
            auto v = next("--json"); if (!v) return false; options.json_path = *v;
        } else if (arg == "--font-dir") {
            auto v = next("--font-dir"); if (!v) return false;
            options.font_dir = *v;
            options.font_dir_given = true;
        } else if (arg == "--canvas") {
            auto v = next("--canvas"); if (!v) return false;
            if (!parse_canvas(*v, options.canvas, error)) return false;
        } else if (arg == "--jobs" || arg == "-j") {
            auto v = next("--jobs"); if (!v) return false;
            if (!parse_jobs(*v, options.jobs, error)) return false;
        } else if (arg == "--strict") {
            options.strict = true;
        } else if (arg == "--help" || arg == "-h") {
            print_usage();
            std::exit(kExitOk);
        } else if (arg.rfind("--canvas=", 0) == 0) {
            if (!parse_canvas(arg.substr(9), options.canvas, error)) return false;
        } else if (arg.rfind("--input=", 0) == 0) {
            options.input = arg.substr(8);
        } else if (arg.rfind("--json=", 0) == 0) {
            options.json_path = arg.substr(7);
        } else if (arg.rfind("--jobs=", 0) == 0) {
            if (!parse_jobs(arg.substr(7), options.jobs, error)) return false;
        } else if (arg.rfind("--font-dir=", 0) == 0) {
            options.font_dir = arg.substr(11);
            options.font_dir_given = true;
        } else {
            error = "unknown argument '" + sanitize_path(arg) + "'";
            return false;
        }
    }
    if (options.input.empty()) {
        error = "--input is required";
        return false;
    }
    return true;
}

std::string libass_version_string() {
    int version = ass_library_version();
    char buf[32];
    std::snprintf(buf, sizeof buf, "%d.%d.%d", (version >> 24) & 0xFF, (version >> 16) & 0xFF,
                  (version >> 8) & 0xFF);
    return buf;
}

}  // namespace

int main(int argc, char** argv) {
    Options options;
    std::string error;
    if (!parse_args(argc, argv, options, error)) {
        std::fprintf(stderr, "error: %s\n\n", error.c_str());
        print_usage();
        return kExitInput;
    }

    std::error_code ec;
    if (!fs::exists(options.input, ec) || !fs::is_regular_file(options.input, ec)) {
        std::fprintf(stderr, "error: input file not found or not a regular file: %s\n",
                     sanitize_path(options.input).c_str());
        return kExitInput;
    }
    if (options.font_dir_given) {
        if (!fs::exists(options.font_dir, ec) || !fs::is_directory(options.font_dir, ec)) {
            std::fprintf(stderr, "error: --font-dir is not a directory: %s\n",
                         sanitize_path(options.font_dir).c_str());
            return kExitInput;
        }
        std::string font_error;
        if (!validate_font_dir_contents(options.font_dir, font_error)) {
            std::fprintf(stderr, "error: %s\n", font_error.c_str());
            return kExitInput;
        }
    }
    if (!options.json_path.empty()) {
        fs::path json_target(options.json_path);
        fs::path parent = json_target.parent_path();
        if (!parent.empty() && !fs::exists(parent, ec) && !fs::is_directory(parent, ec)) {
            std::fprintf(stderr, "error: report directory does not exist: %s\n",
                         sanitize_path(parent.string()).c_str());
            return kExitInput;
        }
        if (fs::is_directory(json_target, ec)) {
            std::fprintf(stderr, "error: --json points at a directory: %s\n",
                         sanitize_path(options.json_path).c_str());
            return kExitInput;
        }
    }
    {
        std::ifstream probe(options.input, std::ios::binary);
        if (!probe) {
            std::fprintf(stderr, "error: cannot open input file: %s\n", sanitize_path(options.input).c_str());
            return kExitInput;
        }
    }

    std::string content;
    {
        std::ifstream in(options.input, std::ios::binary);
        content.assign(std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>());
    }

    sp::Document doc = sp::parse_ass_document(content, options.input);

    sp::Report report;
    report.input_path = options.input;
    report.canvas = options.canvas;
    report.font_dir = options.font_dir;
    report.libass_version = libass_version_string();

    bool fatal_input = false;
    auto push_document_diagnostic = [&](const sp::Diagnostic& d) {
        for (const sp::Diagnostic& existing : report.document_diagnostics) {
            if (existing.code == d.code && existing.line == d.line && existing.message == d.message) return;
        }
        report.document_diagnostics.push_back(d);
        if (d.severity == sp::Severity::Error) fatal_input = true;
    };
    for (const sp::Diagnostic& d : doc.diagnostics) push_document_diagnostic(d);

    std::vector<sp::StyleResolution> resolutions;
    std::vector<sp::Diagnostic> style_diagnostics = sp::resolve_all_styles(doc, resolutions);
    for (const sp::Diagnostic& d : style_diagnostics) push_document_diagnostic(d);

    std::map<std::string, const sp::StyleResolution*> by_name;
    for (const sp::StyleResolution& r : resolutions) by_name[r.requested] = &r;

    std::vector<sp::StyleDefinition> style_defs;
    for (const sp::StyleResolution& r : resolutions)
        style_defs.push_back(sp::StyleDefinition{r.requested, r.style});

    // Build per-event reports up to (but excluding) rendering.
    for (const sp::Event& event : doc.events) {
        sp::EventReport er;
        er.event_id = event.id;
        er.line = event.line;
        er.layer = event.layer;
        er.start_ms = event.start_ms;
        er.end_ms = event.end_ms;
        er.raw_text = event.text;
        er.style_requested = event.style_name;
        er.text = sp::canonicalize_for_render(event.text);
        er.inline_result = sp::parse_inline_tags(event.text, event.id, event.line);
        for (const sp::Diagnostic& d : er.inline_result.diagnostics) er.diagnostics.push_back(d);
        if (er.inline_result.overrides.has_reset) {
            const std::string& target = er.inline_result.overrides.last_reset_target;
            bool found = target.empty();
            for (const sp::RawStyle& raw_style : doc.styles) {
                if (raw_style.name == target) { found = true; break; }
            }
            er.inline_result.overrides.reset_target_missing = !found;
            if (!found) {
                er.diagnostics.push_back(sp::Diagnostic{
                    sp::Severity::Warning, event.line, "reset-style-missing",
                    "override reset names unknown style '" + sp::sanitize_for_output(target, 60) +
                        "'; libass falls back to clearing inline overrides",
                    event.id});
            }
        }

        if (!event.valid) {
            er.skipped = true;
            er.skip_reason = "invalid event fields";
            er.status = "skipped";
            report.events.push_back(std::move(er));
            continue;
        }

        const sp::StyleResolution* resolution = nullptr;
        std::string wanted = event.style_name;
        if (wanted.empty()) {
            wanted = doc.styles.empty() ? std::string() : doc.styles.front().name;
        }
        auto it = by_name.find(wanted);
        if (it != by_name.end()) resolution = it->second;
        if (resolution) {
            er.style_chain = resolution->chain;
            er.cleared_fields = resolution->style.cleared_fields;
            if (resolution->style.font_cleared()) {
                er.diagnostics.push_back(sp::Diagnostic{
                    sp::Severity::Warning, event.line, "style-font-empty",
                    "style chain clears the font name; libass will use its default font", event.id});
            }
        } else {
            // Missing style: libass falls back to its default style. Record it
            // as a locatable diagnostic and continue with the builtin baseline.
            sp::StyleResolution missing = sp::resolve_style(doc, wanted);
            er.style_chain = missing.chain;
            er.cleared_fields = missing.style.cleared_fields;
            er.diagnostics.push_back(sp::Diagnostic{
                sp::Severity::Error, event.line, "style-missing",
                "event references unknown style '" + sp::sanitize_for_output(wanted, 60) + "'", event.id});
        }
        report.events.push_back(std::move(er));
    }

    for (const sp::EventReport& er : report.events) {
        for (const sp::Diagnostic& d : er.diagnostics) {
            if (d.severity == sp::Severity::Error) fatal_input = true;
        }
    }
    if (fatal_input && options.strict) {
        report.summary.events = static_cast<int>(doc.events.size());
        report.summary.styles = static_cast<int>(doc.styles.size());
        report.summary.failed = static_cast<int>(doc.events.size());
        sp::print_terminal_report(report);
        if (!options.json_path.empty()) {
            std::string write_error;
            if (!sp::write_json_report(report, options.json_path, write_error)) {
                std::fprintf(stderr, "error: %s\n", write_error.c_str());
                return kExitReportWrite;
            }
        }
        std::fprintf(stderr, "strict mode: input contains errors, aborting\n");
        return kExitInput;
    }

    // Resolve the effective style for each renderable event.
    std::vector<std::optional<sp::EffStyle>> effective(report.events.size());
    for (size_t i = 0; i < report.events.size(); ++i) {
        sp::EventReport& er = report.events[i];
        if (er.skipped) continue;
        const sp::Event& event = doc.events[i];
        std::string wanted = event.style_name;
        if (wanted.empty() && !doc.styles.empty()) wanted = doc.styles.front().name;
        auto it = by_name.find(wanted);
        if (it != by_name.end()) {
            effective[i] = it->second->style;
            er.adjustments = it->second->style.adjustments;
        } else {
            auto missing = sp::resolve_style(doc, wanted);
            effective[i] = missing.style;
            er.adjustments = missing.style.adjustments;
        }
        size_t before = er.adjustments.size();
        sp::apply_event_margins(*effective[i], event, options.canvas.width, options.canvas.height);
        const auto& appended = effective[i]->adjustments;
        if (appended.size() > before) {
            er.adjustments.insert(er.adjustments.end(), appended.begin() + static_cast<long>(before),
                                  appended.end());
        }
    }

    // Render (optionally in parallel; results are stored by index so the
    // output order always matches the input order).
    sp::RenderOptions render_options;
    render_options.canvas_width = options.canvas.width;
    render_options.canvas_height = options.canvas.height;
    render_options.font_dir = options.font_dir;
    // No default family: a missing font must surface as a fallback failure
    // rather than silently rendering with a substituted face.
    render_options.default_family.clear();

    const int jobs = std::max(1, std::min<int>(options.jobs, static_cast<int>(report.events.size())));
    std::atomic<size_t> next_index{0};
    std::atomic<bool> init_failed{false};
    std::vector<std::string> init_errors(static_cast<size_t>(jobs));

    auto worker = [&](int slot) {
        sp::LayoutProbe probe;
        std::string init_error;
        if (!probe.initialize(render_options, init_error)) {
            init_errors[static_cast<size_t>(slot)] = init_error;
            init_failed = true;
            return;
        }
        probe.set_context(doc.info, style_defs);
        while (true) {
            size_t index = next_index.fetch_add(1);
            if (index >= report.events.size()) break;
            sp::EventReport& er = report.events[index];
            if (er.skipped || !effective[index]) continue;
            const sp::Event& event = doc.events[index];
            er.render = probe.render_event(event, *effective[index], er.style_chain);
            if (!er.render.ok) {
                er.status = "failed";
            } else if (er.render.has_area) {
                er.status = "rendered";
            } else if (er.render.empty_text) {
                er.status = "empty";
            } else {
                er.status = "transparent";
            }
        }
    };

    if (jobs <= 1) {
        worker(0);
    } else {
        std::vector<std::thread> threads;
        threads.reserve(static_cast<size_t>(jobs));
        for (int i = 0; i < jobs; ++i) threads.emplace_back(worker, i);
        for (std::thread& t : threads) t.join();
    }
    if (init_failed) {
        for (const std::string& e : init_errors) {
            if (!e.empty()) {
                std::fprintf(stderr, "error: %s\n", e.c_str());
                break;
            }
        }
        return kExitInput;
    }

    // Aggregate.
    sp::RunSummary& summary = report.summary;
    summary.events = static_cast<int>(report.events.size());
    summary.styles = static_cast<int>(resolutions.size());
    std::string aggregate;
    for (sp::EventReport& er : report.events) {
        if (er.skipped) {
            continue;  // reported separately; not a render failure
        } else if (er.status == "failed") {
            ++summary.failed;
        } else {
            ++summary.rendered;
            if (er.status == "empty") ++summary.empty;
            if (er.status == "transparent") ++summary.transparent;
            if (er.render.clipped) ++summary.clipped;
        }
        summary.nontransparent_pixels += er.render.nontransparent_pixels;
        aggregate += std::to_string(er.event_id) + ":" + er.render.sha256 + ";";
    }
    summary.combined_sha256 = sp::sha256_hex(reinterpret_cast<const unsigned char*>(aggregate.data()),
                                             aggregate.size());

    sp::print_terminal_report(report);

    if (!options.json_path.empty()) {
        std::string write_error;
        if (!sp::write_json_report(report, options.json_path, write_error)) {
            std::fprintf(stderr, "error: %s\n", write_error.c_str());
            return kExitReportWrite;
        }
    }

    if (summary.failed > 0) return kExitPartialRender;
    // Nothing renderable and the document had errors: this is an input error,
    // even when strict mode is off and the remaining events were skipped.
    if (summary.rendered == 0 && fatal_input) return kExitInput;
    return kExitOk;
}
