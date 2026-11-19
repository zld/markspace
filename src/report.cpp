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
    json root;
    root["input"] = report.input_path;
    root["canvas"] = {{"width", report.canvas.width}, {"height", report.canvas.height}};
    root["font_dir"] = report.font_dir;
    root["libass_version"] = report.libass_version;

    json doc_diags = json::array();
    for (const Diagnostic& d : report.document_diagnostics) doc_diags.push_back(diagnostic_json(d));
    root["diagnostics"] = std::move(doc_diags);

    json events = json::array();
    for (const EventReport& e : report.events) {
        json item;
        item["event_id"] = e.event_id;
        item["line"] = e.line;
        item["layer"] = e.layer;
        item["start_ms"] = e.start_ms;
        item["end_ms"] = e.end_ms;
        item["style_requested"] = e.style_requested;
        item["style_chain"] = chain_to_string(e.style_chain);
        item["style_chain_links"] = chain_json(e.style_chain);
        item["inline_tags"] = e.inline_result.counts.to_json();
        item["inline_tags_summary"] = e.inline_result.counts.summary();
        json overrides = json::object();
        for (const auto& [k, v] : e.inline_result.overrides.values) overrides[k] = v;
        item["inline_overrides"] = std::move(overrides);
        item["inline_trace"] = e.inline_result.overrides.trace;
        if (e.inline_result.overrides.has_reset) {
            item["reset"] = {{"target", e.inline_result.overrides.last_reset_target},
                             {"target_missing", e.inline_result.overrides.reset_target_missing},
                             {"applied", true}};
        }
        item["raw_text"] = e.raw_text;
        item["text"] = e.text;
        item["adjustments"] = adjustments_json(e.adjustments);
        item["cleared_fields"] = e.cleared_fields;
        item["status"] = e.status;
        item["skipped"] = e.skipped;
        if (!e.skip_reason.empty()) item["skip_reason"] = e.skip_reason;
        json ev_diags = json::array();
        for (const Diagnostic& d : e.diagnostics) ev_diags.push_back(diagnostic_json(d));
        item["diagnostics"] = std::move(ev_diags);
        item["render"] = render_json(e.render, report.canvas);
        events.push_back(std::move(item));
    }
    root["events"] = std::move(events);

    const RunSummary& s = report.summary;
    root["summary"] = json{{"events", s.events},
                           {"styles", s.styles},
                           {"rendered", s.rendered},
                           {"failed", s.failed},
                           {"empty", s.empty},
                           {"transparent", s.transparent},
                           {"clipped", s.clipped},
                           {"nontransparent_pixels", s.nontransparent_pixels},
                           {"sha256", s.combined_sha256}};
    // Invalid UTF-8 in the source text must not abort the report; substitute
    // U+FFFD instead of throwing.
    return root.dump(2, ' ', false, nlohmann::json::error_handler_t::replace);
}

void print_terminal_report(const Report& report) {
    const RunSummary& s = report.summary;
    std::printf("events=%d styles=%d rendered=%d failed=%d\n", s.events, s.styles, s.rendered, s.failed);
    if (!report.document_diagnostics.empty()) {
        std::printf("\ndocument diagnostics:\n");
        for (const Diagnostic& d : report.document_diagnostics) {
            std::printf("  [%s] line %d %s: %s\n", severity_name(d.severity), d.line, d.code.c_str(),
                        sanitize_for_output(d.message, 200).c_str());
        }
    }
    for (const EventReport& e : report.events) {
        std::printf("\nevent_id=%d line=%d layer=%d start=%.2fs end=%.2fs\n", e.event_id, e.line, e.layer,
                    e.start_ms / 1000.0, e.end_ms / 1000.0);
        std::printf("  style_chain=%s\n", chain_to_string(e.style_chain).c_str());
        std::printf("  inline_tags=%s\n", tag_summary_for_terminal(e.inline_result).c_str());
        if (!e.inline_result.overrides.trace.empty()) {
            std::string trace;
            for (const std::string& step : e.inline_result.overrides.trace) {
                if (!trace.empty()) trace += " ";
                trace += sanitize_for_output(step, 80);
            }
            std::printf("  inline_trace=%s\n", trace.c_str());
        }
        for (const Diagnostic& d : e.diagnostics) {
            std::printf("  [%s] %s: %s\n", severity_name(d.severity), d.code.c_str(),
                        sanitize_for_output(d.message, 200).c_str());
        }
        if (!e.cleared_fields.empty()) {
            std::string cleared;
            for (const std::string& raw_field : e.cleared_fields) {
                const std::string f = sanitize_for_output(raw_field, 40);
                if (!cleared.empty()) cleared += ",";
                cleared += f;
            }
            std::printf("  cleared=%s\n", cleared.c_str());
        }
        for (const Adjustment& a : e.adjustments) {
            std::printf("  adjusted %s: %s -> %s (%s)\n", a.field.c_str(),
                        sanitize_for_output(a.original, 60).c_str(),
                        sanitize_for_output(a.fallback, 100).c_str(),
                        sanitize_for_output(a.source, 60).c_str());
        }
        if (e.skipped) {
            std::printf("  skipped: %s\n", sanitize_for_output(e.skip_reason, 80).c_str());
            continue;
        }
        const RenderResult& r = e.render;
        if (r.has_area) {
            std::printf("  x=%d y=%d width=%d height=%d nontransparent_pixels=%lld\n", r.x, r.y, r.width,
                        r.height, r.nontransparent_pixels);
        } else {
            std::printf("  region=%s x=- y=- width=- height=- nontransparent_pixels=%lld\n",
                        r.empty_text ? "empty" : (r.ok ? "transparent" : "failed"),
                        r.nontransparent_pixels);
        }
        std::printf("  sha256=%s\n", r.sha256.c_str());
        if (r.clipped) std::printf("  note: region touches or exceeds the canvas edge\n");
        for (const std::string& warning : r.font_warnings) {
            std::printf("  font: %s\n", sanitize_for_output(warning, 200).c_str());
        }
        if (!r.ok && !r.error.empty())
            std::printf("  error: %s\n", sanitize_for_output(r.error, 200).c_str());
    }
    std::printf("\nnontransparent_pixels_total=%lld\n", s.nontransparent_pixels);
    std::printf("sha256_total=%s\n", s.combined_sha256.c_str());
}

bool write_json_report(const Report& report, const std::string& path, std::string& error) {
    namespace fs = std::filesystem;
    std::error_code ec;
    fs::path target(path);
    fs::path parent = target.parent_path();
    std::random_device rd;
    std::string tmp_name = target.filename().string() + ".tmp-" + std::to_string(rd());
    fs::path tmp = parent.empty() ? fs::path(tmp_name) : parent / tmp_name;
    {
        std::ofstream out(tmp, std::ios::binary | std::ios::trunc);
        if (!out) {
            error = "cannot open temporary report file '" + tmp.string() + "'";
            return false;
        }
        std::string payload = build_json(report);
        payload += "\n";
        out.write(payload.data(), static_cast<std::streamsize>(payload.size()));
        out.flush();
        if (!out) {
            out.close();
            fs::remove(tmp, ec);
            error = "failed while writing temporary report file";
            return false;
        }
    }
    fs::rename(tmp, target, ec);
    if (ec) {
        fs::remove(tmp, ec);
        error = "cannot atomically replace '" + path + "': " + ec.message();
        return false;
    }
    return true;
}

}  // namespace sp
