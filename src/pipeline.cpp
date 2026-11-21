#include "pipeline.hpp"

#include <filesystem>
#include <fstream>
#include <sstream>

#include "fixture_builder.hpp"
#include "layout_engine.hpp"
#include "renderer.hpp"
#include "style_parser.hpp"
#include "tag_resolver.hpp"

namespace asspipe {

namespace fs = std::filesystem;

namespace {

std::string json_escape(const std::string& s) {
    std::string out;
    out.reserve(s.size() + 2);
    for (char ch : s) {
        switch (ch) {
        case '"': out += "\\\""; break;
        case '\\': out += "\\\\"; break;
        case '\n': out += "\\n"; break;
        case '\r': out += "\\r"; break;
        case '\t': out += "\\t"; break;
        default:
            if (static_cast<unsigned char>(ch) < 0x20) {
                char buf[8];
                std::snprintf(buf, sizeof(buf), "\\u%04x",
                              static_cast<unsigned char>(ch));
                out += buf;
            } else {
                out.push_back(ch);
            }
        }
    }
    return out;
}

bool same_path(const fs::path& a, const fs::path& b) {
    std::error_code ec;
    if (a.empty() || b.empty()) return false;
    if (fs::exists(a, ec) && fs::exists(b, ec))
        return fs::equivalent(a, b, ec);
    return fs::weakly_canonical(a, ec) == fs::weakly_canonical(b, ec);
}

std::string read_file_limited(const fs::path& path, bool& ok) {
    ok = false;
    std::error_code ec;
    auto size = fs::file_size(path, ec);
    if (ec) return {};
    if (size > MAX_INPUT_BYTES) return {};
    std::ifstream f(path, std::ios::binary);
    if (!f) return {};
    std::ostringstream ss;
    ss << f.rdbuf();
    std::string data = ss.str();
    if (data.size() > MAX_INPUT_BYTES) return {};
    ok = true;
    return data;
}

bool has_error_diagnostics(const AssDocument& doc) {
    for (const auto& d : doc.diagnostics)
        if (d.level == DiagLevel::Error) return true;
    return false;
}

// Write the JSON manifest next to the rendered output. Failures are recorded
// as diagnostics rather than thrown, keeping the pipeline no-throw.
void write_manifest(const PipelineOptions& opts, const PipelineResult& result,
                    PipelineResult& io_result) {
    if (opts.json_path.empty()) return;
    std::error_code ec;
    fs::create_directories(fs::path(opts.json_path).parent_path(), ec);
    std::ofstream jf(opts.json_path, std::ios::binary);
    if (!jf) {
        io_result.diagnostics.push_back(
            {DiagLevel::Error, DiagKind::PathRejected, 0, 0,
             "cannot write JSON manifest: " + opts.json_path});
        if (io_result.exit_code == EXIT_OK)
            io_result.exit_code = EXIT_INVALID_INPUT;
        return;
    }
    jf << render_json(result);
}

} // namespace

PipelineResult run_pipeline(const PipelineOptions& opts) {
    PipelineResult result;

    // ---- Input acquisition -------------------------------------------
    std::string ass_text;
    std::string source_name = "demo.ass";
    if (opts.command == "demo") {
        ass_text = build_demo_ass();
    } else {
        source_name = opts.input_path;
        bool read_ok = false;
        ass_text = read_file_limited(opts.input_path, read_ok);
        if (!read_ok) {
            result.exit_code = EXIT_INVALID_INPUT;
            result.diagnostics.push_back(
                {DiagLevel::Error,
                 ass_text.empty() ? DiagKind::InputTooLarge
                                  : DiagKind::BadFieldCount,
                 0, 0,
                 "cannot read input (missing or larger than 32 MiB): " +
                     opts.input_path});
            write_manifest(opts, result, result);
            return result;
        }
    }

    // ---- Parsing, inheritance and tag statistics ----------------------
    AssDocument doc = parse_ass(ass_text, source_name);
    build_inheritance_graph(doc);
    result.inline_tags = 0;
    result.unknown_tags = 0;
    for (auto& ev : doc.events) {
        resolve_event_tags(ev);
        result.inline_tags += ev.tags.total();
        result.unknown_tags += ev.tags.unknown;
    }
    result.styles = static_cast<int>(doc.styles.size());
    result.events = static_cast<int>(doc.events.size());
    result.inheritance_errors = doc.inheritance_errors;
    result.diagnostics = doc.diagnostics;

    // Blocking parse errors (anything other than an inheritance defect)
    // invalidate the input, but are returned only after the output
    // directory has been prepared so the JSON manifest can still be written.
    bool blocking_input_error = false;
    if (has_error_diagnostics(doc)) {
        for (const auto& d : doc.diagnostics)
            if (d.level == DiagLevel::Error &&
                d.kind != DiagKind::MissingParent &&
                d.kind != DiagKind::SelfInheritance &&
                d.kind != DiagKind::InheritanceCycle)
                blocking_input_error = true;
    }

    // ---- Output directory safety --------------------------------------
    std::error_code ec;
    fs::path out_dir(opts.output_dir);
    if (!out_dir.empty() && fs::exists(opts.input_path, ec) &&
        same_path(out_dir, fs::path(opts.input_path))) {
        result.exit_code = EXIT_INVALID_INPUT;
        result.diagnostics.push_back(
            {DiagLevel::Error, DiagKind::PathRejected, 0, 0,
             "output directory must not be the input file: " +
                 opts.output_dir});
        write_manifest(opts, result, result);
        return result;
    }
    if (!opts.json_path.empty() && fs::exists(opts.input_path, ec) &&
        same_path(fs::path(opts.json_path), fs::path(opts.input_path))) {
        result.exit_code = EXIT_INVALID_INPUT;
        result.diagnostics.push_back(
            {DiagLevel::Error, DiagKind::PathRejected, 0, 0,
             "refusing to overwrite input file with JSON manifest"});
        write_manifest(opts, result, result);
        return result;
    }
    if (!out_dir.empty()) {
        fs::create_directories(out_dir, ec);
        if (ec) {
            result.exit_code = EXIT_INVALID_INPUT;
            result.diagnostics.push_back(
                {DiagLevel::Error, DiagKind::PathRejected, 0, 0,
                 "cannot create output directory: " + opts.output_dir});
            write_manifest(opts, result, result);
            return result;
        }
    }

    // ---- Rendering -----------------------------------------------------
    if (blocking_input_error) {
        result.exit_code = EXIT_INVALID_INPUT;
        write_manifest(opts, result, result);
        return result;
    }

    LayoutConfig lcfg{opts.canvas, opts.fonts_dir};
    LayoutEngine engine(lcfg);
    bool render_failed = false;
    for (const auto& ev : doc.events) {
        RenderedEvent rendered;
        std::string error;
        std::vector<Diagnostic> diags;
        if (!engine.render_event(doc, ev, rendered, error, diags)) {
            for (auto& d : diags) result.diagnostics.push_back(d);
            render_failed = true;
            continue;
        }
        std::vector<std::uint8_t> png;
        if (!encode_png(rendered.rgba, opts.canvas.width, opts.canvas.height,
                        png)) {
            result.diagnostics.push_back(
                {DiagLevel::Error, DiagKind::RenderFailure, ev.line, ev.index,
                 "PNG encoding failed for event " +
                     std::to_string(ev.index)});
            render_failed = true;
            continue;
        }
        std::string png_path = event_png_path(opts.output_dir, ev.index);
        std::ofstream pf(png_path, std::ios::binary);
        if (!pf) {
            result.diagnostics.push_back(
                {DiagLevel::Error, DiagKind::RenderFailure, ev.line, ev.index,
                 "cannot write PNG: " + png_path});
            render_failed = true;
            continue;
        }
        pf.write(reinterpret_cast<const char*>(png.data()),
                 static_cast<std::streamsize>(png.size()));

        EventRecord rec;
        rec.event_id = rendered.event_id;
        rec.x = rendered.x;
        rec.y = rendered.y;
        rec.width = rendered.width;
        rec.height = rendered.height;
        rec.sha256 = sha256_hex(png);
        rec.png_path = png_path;
        result.records.push_back(rec);
    }
    result.rendered = static_cast<int>(result.records.size());
    if (render_failed) result.exit_code = EXIT_RENDER_FAILURE;
    write_manifest(opts, result, result);
    return result;
}

std::string render_json(const PipelineResult& r) {
    std::ostringstream os;
    os << "{\n";
    os << "  \"styles\": " << r.styles << ",\n";
    os << "  \"events\": " << r.events << ",\n";
    os << "  \"inheritance_errors\": " << r.inheritance_errors << ",\n";
    os << "  \"inline_tags\": " << r.inline_tags << ",\n";
    os << "  \"unknown_tags\": " << r.unknown_tags << ",\n";
    os << "  \"rendered\": " << r.rendered << ",\n";
    os << "  \"exit_code\": " << r.exit_code << ",\n";
    os << "  \"records\": [\n";
    for (std::size_t i = 0; i < r.records.size(); ++i) {
        const auto& e = r.records[i];
        os << "    {\"event_id\": " << e.event_id << ", \"x\": " << e.x
           << ", \"y\": " << e.y << ", \"width\": " << e.width
           << ", \"height\": " << e.height << ", \"sha256\": \""
           << e.sha256 << "\", \"png_path\": \""
           << json_escape(e.png_path) << "\"}"
           << (i + 1 == r.records.size() ? "" : ",") << "\n";
    }
    os << "  ],\n  \"diagnostics\": [\n";
    for (std::size_t i = 0; i < r.diagnostics.size(); ++i) {
        const auto& d = r.diagnostics[i];
        os << "    {\"level\": \""
           << (d.level == DiagLevel::Error ? "error" : "warning")
           << "\", \"category\": \"" << d.category() << "\", \"line\": "
           << d.line << ", \"event\": " << d.event_index << ", \"message\": \""
           << json_escape(d.message) << "\"}"
           << (i + 1 == r.diagnostics.size() ? "" : ",") << "\n";
    }
    os << "  ]\n}\n";
    return os.str();
}

} // namespace asspipe
