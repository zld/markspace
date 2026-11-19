#include "ass_document.hpp"

#include <algorithm>
#include <cctype>
#include <cstdio>
#include <cstring>

namespace sp {

const char* severity_name(Severity severity) {
    return severity == Severity::Error ? "error" : "warning";
}

std::string sanitize_for_output(const std::string& text, size_t max_len) {
    std::string out;
    out.reserve(std::min(text.size(), max_len));
    bool last_was_escape_marker = false;
    for (unsigned char ch : text) {
        if (out.size() >= max_len) { out += "..."; break; }
        if (ch < 0x20 || ch == 0x7f) {
            out += '?';
            last_was_escape_marker = false;
        } else {
            out += static_cast<char>(ch);
            last_was_escape_marker = false;
        }
    }
    (void)last_was_escape_marker;
    return out;
}

std::string trim_ascii(const std::string& text) {
    size_t begin = 0;
    size_t end = text.size();
    while (begin < end && (text[begin] == ' ' || text[begin] == '\t' || text[begin] == '\r' || text[begin] == '\n'))
        ++begin;
    while (end > begin && (text[end - 1] == ' ' || text[end - 1] == '\t' || text[end - 1] == '\r' || text[end - 1] == '\n'))
        --end;
    return text.substr(begin, end - begin);
}

std::string lower_ascii(std::string text) {
    for (char& ch : text)
        ch = static_cast<char>(std::tolower(static_cast<unsigned char>(ch)));
    return text;
}

bool parse_ass_time(const std::string& raw, long long& out_ms) {
    std::string text = trim_ascii(raw);
    if (text.empty()) return false;
    // Accept H:MM:SS.cc and H:MM:SS.mmm (and a bare numeric fallback).
    size_t c1 = text.find(':');
    if (c1 == std::string::npos) return false;
    size_t c2 = text.find(':', c1 + 1);
    if (c2 == std::string::npos) return false;
    std::string h = text.substr(0, c1);
    std::string m = text.substr(c1 + 1, c2 - c1 - 1);
    std::string rest = text.substr(c2 + 1);
    size_t dot = rest.find('.');
    std::string s = dot == std::string::npos ? rest : rest.substr(0, dot);
    std::string frac = dot == std::string::npos ? "" : rest.substr(dot + 1);
    auto digits = [](const std::string& v) {
        return !v.empty() && std::all_of(v.begin(), v.end(), [](unsigned char c) { return std::isdigit(c) != 0; });
    };
    if (!digits(h) || !digits(m) || !digits(s)) return false;
    if (!frac.empty() && !digits(frac)) return false;
    if (frac.size() > 3) return false;
    long long hh = std::stoll(h);
    long long mm = std::stoll(m);
    long long ss = std::stoll(s);
    if (mm > 59 || ss > 59) return false;
    long long ms = 0;
    if (!frac.empty()) {
        std::string padded = frac;
        while (padded.size() < 3) padded += '0';
        ms = std::stoll(padded);
    }
    out_ms = ((hh * 60 + mm) * 60 + ss) * 1000 + ms;
    return true;
}

std::vector<std::string> split_commas(const std::string& text, size_t max_fields) {
    std::vector<std::string> fields;
    std::string current;
    for (char ch : text) {
        if (ch == ',' && (max_fields == 0 || fields.size() + 1 < max_fields)) {
            fields.push_back(current);
            current.clear();
        } else {
            current += ch;
        }
    }
    fields.push_back(current);
    return fields;
}

bool Document::has_error() const {
    return std::any_of(diagnostics.begin(), diagnostics.end(),
                       [](const Diagnostic& d) { return d.severity == Severity::Error; });
}

namespace {

struct Section {
    enum Kind { None, ScriptInfo, Styles, Events, Other } kind = None;
    std::string name;
};

std::vector<std::string> normalize_format(const std::string& line, const char* prefix) {
    std::string rest = trim_ascii(line.substr(std::strlen(prefix)));
    std::vector<std::string> out;
    for (const std::string& field : split_commas(rest, 0))
        out.push_back(lower_ascii(trim_ascii(field)));
    return out;
}

void add_diag(Document& doc, Severity severity, int line, const char* code, std::string message, int event_id = -1) {
    doc.diagnostics.push_back(Diagnostic{severity, line, code, std::move(message), event_id});
}

RawStyle parse_style_line(Document& doc, const std::string& line, int line_no,
                          const std::vector<std::string>& format) {
    RawStyle style;
    style.line = line_no;
    std::string rest = trim_ascii(line.substr(std::string("Style:").size()));
    std::vector<std::string> fields = split_commas(rest, 0);
    if (format.empty()) {
        add_diag(doc, Severity::Error, line_no, "style-format-missing",
                 "Style: line appears before a Format: line in [V4+ Styles]");
        return style;
    }
    if (fields.size() != format.size()) {
        add_diag(doc, Severity::Error, line_no, "style-field-count",
                 "Style: has " + std::to_string(fields.size()) + " fields but Format declares " +
                     std::to_string(format.size()));
    }
    // A blank column normally means "inherit from the parent". To keep an
    // explicit empty distinct from "unspecified", the fixture format writes an
    // explicit empty as a quoted empty string (""), which clears the field.
    for (size_t i = 0; i < format.size() && i < fields.size(); ++i) {
        std::string raw = trim_ascii(fields[i]);
        FieldValue value;
        if (raw == "\"\"") {
            value.present = true;
            value.explicit_empty = true;
        } else if (!raw.empty()) {
            value.present = true;
            value.value = raw;
        }
        style.fields[format[i]] = value;
    }
    if (style.fields.count("name") && style.fields["name"].present)
        style.name = style.fields["name"].value;
    if (style.fields.count("parent") && style.fields["parent"].present)
        style.parent = style.fields["parent"].value;
    if (style.name.empty()) {
        add_diag(doc, Severity::Error, line_no, "style-name-missing", "Style: line has no Name field");
        style.name = "<unnamed@" + std::to_string(line_no) + ">";
    }
    return style;
}

Event parse_event_line(Document& doc, const std::string& line, int line_no,
                       const std::vector<std::string>& format, const std::string& kind) {
    Event event;
    event.line = line_no;
    event.id = static_cast<int>(doc.events.size());
    const std::string prefix = kind + ":";
    std::string rest = trim_ascii(line.substr(prefix.size()));
    std::vector<std::string> fields = split_commas(rest, format.empty() ? 0 : format.size());
    if (format.empty()) {
        add_diag(doc, Severity::Error, line_no, "event-format-missing",
                 kind + ": line appears before a Format: line in [Events]", event.id);
        event.valid = false;
        return event;
    }
    if (fields.size() != format.size()) {
        add_diag(doc, Severity::Error, line_no, "event-field-count",
                 kind + ": has " + std::to_string(fields.size()) + " fields but Format declares " +
                     std::to_string(format.size()),
                 event.id);
        // Best effort: keep the trailing field as text so the report still
        // shows what the line contained.
        if (!fields.empty()) event.text = fields.back();
        event.valid = false;
        return event;
    }
    std::map<std::string, std::string> values;
    for (size_t i = 0; i < format.size(); ++i) values[format[i]] = fields[i];

    auto grab = [&](const char* key, std::string& out) {
        auto it = values.find(key);
        if (it != values.end()) out = trim_ascii(it->second);
    };
    auto grab_int = [&](const char* key, int& out, bool& ok) {
        auto it = values.find(key);
        if (it == values.end()) return;
        std::string v = trim_ascii(it->second);
        if (v.empty()) { out = 0; return; }
        try {
            size_t used = 0;
            long parsed = std::stol(v, &used);
            if (used != v.size()) throw std::invalid_argument("trailing");
            out = static_cast<int>(parsed);
        } catch (...) {
            add_diag(doc, Severity::Error, line_no, "event-field-invalid",
                     kind + ": field '" + key + "' is not an integer: '" + sanitize_for_output(v, 40) + "'",
                     event.id);
            ok = false;
        }
    };

    bool ok = true;
    grab_int("layer", event.layer, ok);
    grab("style", event.style_name);
    grab("name", event.name);
    grab("effect", event.effect);
    grab_int("marginl", event.margin_l, ok);
    grab_int("marginr", event.margin_r, ok);
    grab_int("marginv", event.margin_v, ok);
    std::string start_raw, end_raw;
    grab("start", start_raw);
    grab("end", end_raw);
    if (!parse_ass_time(start_raw, event.start_ms)) {
        add_diag(doc, Severity::Error, line_no, "event-time-invalid",
                 kind + ": invalid start timestamp '" + sanitize_for_output(start_raw, 40) + "'", event.id);
        ok = false;
    }
    if (!parse_ass_time(end_raw, event.end_ms)) {
        add_diag(doc, Severity::Error, line_no, "event-time-invalid",
                 kind + ": invalid end timestamp '" + sanitize_for_output(end_raw, 40) + "'", event.id);
        ok = false;
    }
    if (ok && event.end_ms < event.start_ms) {
        add_diag(doc, Severity::Error, line_no, "event-time-inverted",
                 kind + ": end timestamp precedes start timestamp", event.id);
        ok = false;
    }
    event.duration_ms = event.end_ms - event.start_ms;
    auto text_it = values.find("text");
    event.text = text_it == values.end() ? "" : text_it->second;
    if (event.style_name.empty()) {
        add_diag(doc, Severity::Warning, line_no, "event-style-empty",
                 kind + ": empty Style field, falling back to default style", event.id);
    }
    event.valid = ok;
    return event;
}

}  // namespace

Document parse_ass_document(const std::string& content, const std::string& source_name) {
    Document doc;
    doc.source_name = source_name;
    Section section;
    std::vector<std::string> style_format;
    std::vector<std::string> event_format;
    std::map<std::string, int> style_name_lines;

    size_t pos = 0;
    int line_no = 0;
    while (pos <= content.size()) {
        size_t nl = content.find('\n', pos);
        std::string raw = content.substr(pos, nl == std::string::npos ? std::string::npos : nl - pos);
        if (!raw.empty() && raw.back() == '\r') raw.pop_back();
        ++line_no;
        if (nl == std::string::npos && pos >= content.size()) break;
        pos = nl == std::string::npos ? content.size() + 1 : nl + 1;

        std::string line = trim_ascii(raw);
        if (line.empty() || line[0] == ';') continue;

        if (line.size() >= 2 && line.front() == '[' && line.back() == ']') {
            std::string name = lower_ascii(trim_ascii(line.substr(1, line.size() - 2)));
            if (name == "script info") section.kind = Section::ScriptInfo;
            else if (name == "v4+ styles" || name == "v4 styles") section.kind = Section::Styles;
            else if (name == "events") section.kind = Section::Events;
            else section.kind = Section::Other;
            section.name = name;
            continue;
        }

        std::string lower = lower_ascii(line);
        if (section.kind == Section::ScriptInfo) {
            size_t colon = line.find(':');
            if (colon == std::string::npos) continue;
            std::string key = lower_ascii(trim_ascii(line.substr(0, colon)));
            std::string value = trim_ascii(line.substr(colon + 1));
            if (key == "playresx") {
                try { doc.info.play_res_x = std::stoi(value); } catch (...) {
                    add_diag(doc, Severity::Warning, line_no, "script-info-invalid", "PlayResX is not an integer");
                }
            } else if (key == "playresy") {
                try { doc.info.play_res_y = std::stoi(value); } catch (...) {
                    add_diag(doc, Severity::Warning, line_no, "script-info-invalid", "PlayResY is not an integer");
                }
            } else if (key == "wrapstyle") {
                try { doc.info.wrap_style = std::stoi(value); } catch (...) {}
            } else if (key == "scaledborderandshadow") {
                std::string lv = lower_ascii(value);
                doc.info.scaled_border_and_shadow = (lv == "yes" || lv == "1" || lv == "true");
            } else if (key == "ycbcr matrix") {
                doc.info.ycbcr_matrix = value;
            }
            continue;
        }

        if (section.kind == Section::Styles) {
            if (lower.rfind("format:", 0) == 0) {
                style_format = normalize_format(line, "Format:");
                continue;
            }
            if (lower.rfind("style:", 0) == 0) {
                RawStyle style = parse_style_line(doc, line, line_no, style_format);
                ++doc.style_line_count;
                auto existing = style_name_lines.find(style.name);
                if (existing != style_name_lines.end()) {
                    add_diag(doc, Severity::Error, line_no, "style-duplicate-name",
                             "duplicate style name '" + sanitize_for_output(style.name, 60) +
                                 "' (first defined on line " + std::to_string(existing->second) + ")");
                    continue;
                }
                style_name_lines[style.name] = line_no;
                doc.styles.push_back(std::move(style));
                continue;
            }
            continue;
        }

        if (section.kind == Section::Events) {
            if (lower.rfind("format:", 0) == 0) {
                event_format = normalize_format(line, "Format:");
                continue;
            }
            if (lower.rfind("dialogue:", 0) == 0) {
                doc.events.push_back(parse_event_line(doc, line, line_no, event_format, "Dialogue"));
                continue;
            }
            if (lower.rfind("comment:", 0) == 0) {
                ++doc.comment_count;
                continue;
            }
            // Unknown event-ish keyword inside [Events]: report with line number.
            size_t colon = line.find(':');
            if (colon != std::string::npos) {
                std::string kind = line.substr(0, colon);
                bool known = lower.rfind("picture:", 0) == 0 || lower.rfind("sound:", 0) == 0 ||
                             lower.rfind("movie:", 0) == 0 || lower.rfind("command:", 0) == 0;
                add_diag(doc, known ? Severity::Warning : Severity::Error, line_no,
                         known ? "event-unsupported" : "event-unknown",
                         "unknown event type '" + sanitize_for_output(kind, 40) + "' in [Events]");
            }
            continue;
        }
    }
    return doc;
}

}  // namespace sp
