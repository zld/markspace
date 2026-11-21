#include "style_parser.hpp"

#include <algorithm>
#include <cctype>
#include <sstream>

namespace asspipe {

namespace {

std::string lower(std::string s) {
    std::transform(s.begin(), s.end(), s.begin(),
                   [](unsigned char c) { return static_cast<char>(std::tolower(c)); });
    return s;
}

bool is_int(const std::string& s) {
    if (s.empty()) return false;
    std::size_t i = (s[0] == '-') ? 1 : 0;
    if (i == s.size()) return false;
    for (; i < s.size(); ++i)
        if (std::isdigit(static_cast<unsigned char>(s[i])) == 0) return false;
    return true;
}

int to_int(const std::string& s, int fallback) {
    return is_int(s) ? std::stoi(s) : fallback;
}

double to_double(const std::string& s, double fallback) {
    try {
        std::size_t used = 0;
        double v = std::stod(s, &used);
        return used == s.size() ? v : fallback;
    } catch (...) {
        return fallback;
    }
}

void add_diag(AssDocument& doc, DiagLevel level, DiagKind kind, int line,
              int event_index, std::string message) {
    doc.diagnostics.push_back(
        Diagnostic{level, kind, line, event_index, std::move(message)});
}

// Section names recognised by the parser.
enum class Section { None, ScriptInfo, Styles, Events, Inheritance };

Section section_of(const std::string& header) {
    std::string h = lower(trim_copy(header));
    if (h == "[script info]") return Section::ScriptInfo;
    if (h == "[v4+ styles]" || h == "[v4 styles]") return Section::Styles;
    if (h == "[events]") return Section::Events;
    if (h == "[inheritance]") return Section::Inheritance;
    return Section::None;
}

// Parse "Format: a, b, c" into a trimmed, lower-cased field-name vector.
std::vector<std::string> parse_format(const std::string& body) {
    std::vector<std::string> names;
    for (auto& f : split_fields(body, 100)) {
        names.push_back(lower(trim_copy(f)));
    }
    return names;
}

int index_of(const std::vector<std::string>& names, const std::string& key) {
    for (std::size_t i = 0; i < names.size(); ++i)
        if (names[i] == key) return static_cast<int>(i);
    return -1;
}

std::string field_at(const std::vector<std::string>& fields, int idx) {
    if (idx < 0 || static_cast<std::size_t>(idx) >= fields.size())
        return std::string{};
    return trim_copy(fields[static_cast<std::size_t>(idx)]);
}

} // namespace

std::string& trim(std::string& s) {
    auto not_space = [](unsigned char ch) { return std::isspace(ch) == 0; };
    s.erase(s.begin(), std::find_if(s.begin(), s.end(), not_space));
    s.erase(std::find_if(s.rbegin(), s.rend(), not_space).base(), s.end());
    return s;
}

std::string trim_copy(std::string s) { return trim(s); }

std::vector<std::string> split_fields(const std::string& line,
                                      std::size_t max_fields) {
    std::vector<std::string> fields;
    std::size_t start = 0;
    if (max_fields == 0) return fields;
    while (fields.size() + 1 < max_fields) {
        auto pos = line.find(',', start);
        if (pos == std::string::npos) break;
        fields.push_back(line.substr(start, pos - start));
        start = pos + 1;
    }
    fields.push_back(line.substr(start));
    return fields;
}

bool parse_timestamp(const std::string& token, long& out_centiseconds) {
    long h = 0, m = 0, s = 0, cs = 0;
    char c1 = 0, c2 = 0, c3 = 0;
    std::istringstream iss(token);
    iss >> h >> c1 >> m >> c2 >> s >> c3 >> cs;
    if (iss.fail() || !iss.eof() || c1 != ':' || c2 != ':' || c3 != '.')
        return false;
    if (m < 0 || m > 59 || s < 0 || s > 59 || cs < 0 || cs > 99 || h < 0)
        return false;
    out_centiseconds = ((h * 3600 + m * 60 + s) * 100) + cs;
    return true;
}

AssDocument parse_ass(const std::string& text, const std::string& source_name) {
    AssDocument doc;
    if (text.size() > MAX_INPUT_BYTES) {
        add_diag(doc, DiagLevel::Error, DiagKind::InputTooLarge, 0, 0,
                 source_name + ": input exceeds 32 MiB limit");
        return doc;
    }

    // Split into physical lines, stripping a UTF-8 BOM and CR characters.
    std::vector<std::string> lines;
    std::string current;
    for (char ch : text) {
        if (ch == '\r') continue;
        if (ch == '\n') {
            lines.push_back(current);
            current.clear();
        } else {
            current.push_back(ch);
        }
    }
    if (!current.empty()) lines.push_back(current);
    if (!lines.empty() && !lines.front().empty() &&
        static_cast<unsigned char>(lines.front()[0]) == 0xEF) {
        const std::string bom = "\xEF\xBB\xBF";
        if (lines.front().compare(0, bom.size(), bom) == 0)
            lines.front().erase(0, bom.size());
    }

    Section section = Section::None;
    std::vector<std::string> style_format;
    std::vector<std::string> event_format;
    const std::vector<std::string> default_style_format = {
        "name",       "fontname",  "fontsize", "primarycolour",
        "secondarycolour", "outlinecolour", "backcolour", "bold",
        "italic",     "underline", "strikeout", "scalex",
        "scaley",     "spacing",   "angle",    "borderstyle",
        "outline",    "shadow",    "alignment", "marginl",
        "marginr",    "marginv",   "encoding"};
    const std::vector<std::string> default_event_format = {
        "layer", "start", "end", "style", "name",
        "marginl", "marginr", "marginv", "effect", "text"};

    int event_count = 0;
    bool limit_reported = false;
    for (int lineno = 1; lineno <= static_cast<int>(lines.size()); ++lineno) {
        std::string raw = lines[static_cast<std::size_t>(lineno) - 1];
        std::string line = trim_copy(raw);
        if (line.empty() || line[0] == ';') continue;

        if (line[0] == '[') {
            Section next = section_of(line);
            if (next == Section::None)
                add_diag(doc, DiagLevel::Warning, DiagKind::InvalidSection,
                         lineno, 0, "unknown section header: " + line);
            section = next;
            continue;
        }

        auto colon = line.find(':');
        if (colon == std::string::npos) continue;
        std::string keyword = lower(trim_copy(line.substr(0, colon)));
        std::string body = line.substr(colon + 1);

        if (section == Section::Styles) {
            if (keyword == "format") {
                style_format = parse_format(body);
                continue;
            }
            if (keyword != "style") continue;
            const auto& names = style_format.empty() ? default_style_format
                                                      : style_format;
            auto fields = split_fields(body, names.size());
            if (fields.size() < names.size())
                add_diag(doc, DiagLevel::Error, DiagKind::BadFieldCount,
                         lineno, 0,
                         "style row has " + std::to_string(fields.size()) +
                             " fields, expected " +
                             std::to_string(names.size()));
            Style st;
            st.line = lineno;
            st.name = field_at(fields, index_of(names, "name"));
            st.fontname = field_at(fields, index_of(names, "fontname"));
            st.fontsize = to_int(field_at(fields, index_of(names, "fontsize")), 0);
            st.primary_colour = field_at(fields, index_of(names, "primarycolour"));
            st.secondary_colour = field_at(fields, index_of(names, "secondarycolour"));
            st.outline_colour = field_at(fields, index_of(names, "outlinecolour"));
            st.back_colour = field_at(fields, index_of(names, "backcolour"));
            st.bold = to_int(field_at(fields, index_of(names, "bold")), -1);
            st.italic = to_int(field_at(fields, index_of(names, "italic")), -1);
            st.border_style = to_int(field_at(fields, index_of(names, "borderstyle")), 1);
            st.outline = to_double(field_at(fields, index_of(names, "outline")), 0.0);
            st.shadow = to_double(field_at(fields, index_of(names, "shadow")), 0.0);
            st.alignment = to_int(field_at(fields, index_of(names, "alignment")), 2);
            st.margin_l = to_int(field_at(fields, index_of(names, "marginl")), -1);
            st.margin_r = to_int(field_at(fields, index_of(names, "marginr")), -1);
            st.margin_v = to_int(field_at(fields, index_of(names, "marginv")), -1);
            st.encoding = to_int(field_at(fields, index_of(names, "encoding")), 1);
            if (st.name.empty()) {
                add_diag(doc, DiagLevel::Error, DiagKind::BadFieldCount,
                         lineno, 0, "style with empty name ignored");
                continue;
            }
            doc.styles.emplace(lower(st.name), std::move(st));
            continue;
        }

        if (section == Section::Events) {
            if (keyword == "format") {
                event_format = parse_format(body);
                continue;
            }
            if (keyword != "dialogue") continue; // Comment and others ignored
            const auto& names = event_format.empty() ? default_event_format
                                                      : event_format;
            auto fields = split_fields(body, names.size());
            if (fields.size() < names.size())
                add_diag(doc, DiagLevel::Error, DiagKind::BadFieldCount,
                         lineno, 0,
                         "dialogue row has " + std::to_string(fields.size()) +
                             " fields, expected " +
                             std::to_string(names.size()));
            Event ev;
            ev.line = lineno;
            ev.index = ++event_count;
            std::string start = field_at(fields, index_of(names, "start"));
            std::string end = field_at(fields, index_of(names, "end"));
            if (!parse_timestamp(start, ev.start_cs))
                add_diag(doc, DiagLevel::Error, DiagKind::BadTimestamp,
                         lineno, ev.index, "invalid start timestamp: " + start);
            if (!parse_timestamp(end, ev.end_cs))
                add_diag(doc, DiagLevel::Error, DiagKind::BadTimestamp,
                         lineno, ev.index, "invalid end timestamp: " + end);
            ev.style_name = field_at(fields, index_of(names, "style"));
            ev.text = field_at(fields, index_of(names, "text"));
            if (doc.events.size() >= MAX_EVENTS) {
                // Report the overflow once; every further Dialogue row is
                // silently dropped rather than spamming diagnostics.
                if (!limit_reported) {
                    add_diag(doc, DiagLevel::Error, DiagKind::TooManyEvents,
                             lineno, ev.index,
                             "event limit of 10000 exceeded; remaining "
                             "events dropped");
                    limit_reported = true;
                }
                continue;
            }
            doc.events.push_back(std::move(ev));
            continue;
        }

        if (section == Section::Inheritance && keyword == "inherit") {
            // Body form: "ChildStyle = ParentStyle"
            auto eq = body.find('=');
            if (eq == std::string::npos) {
                add_diag(doc, DiagLevel::Error, DiagKind::BadFieldCount,
                         lineno, 0, "inherit line needs Child=Parent");
                continue;
            }
            std::string child = trim_copy(body.substr(0, eq));
            std::string parent = trim_copy(body.substr(eq + 1));
            auto it = doc.styles.find(lower(child));
            if (it == doc.styles.end()) {
                add_diag(doc, DiagLevel::Error, DiagKind::MissingParent,
                         lineno, 0,
                         "inherit target child style not found: " + child);
                continue;
            }
            it->second.parent = parent;
            continue;
        }
    }

    return doc;
}

} // namespace asspipe
