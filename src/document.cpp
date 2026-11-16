#include "document.hpp"

#include <cctype>
#include <cstdlib>

namespace subaudit {
namespace {

std::string trim(const std::string& s) {
    std::size_t a = 0, b = s.size();
    while (a < b && std::isspace(static_cast<unsigned char>(s[a]))) ++a;
    while (b > a && std::isspace(static_cast<unsigned char>(s[b - 1]))) --b;
    return s.substr(a, b - a);
}

// Splits a value line into at most max_cols comma-separated columns; the
// final column keeps any remaining commas (the ASS Text rule).
std::vector<std::string> split_columns(const std::string& line, std::size_t max_cols,
                                       bool trim_last) {
    std::vector<std::string> cols;
    std::size_t pos = 0;
    while (cols.size() + 1 < max_cols) {
        std::size_t comma = line.find(',', pos);
        if (comma == std::string::npos) break;
        cols.push_back(trim(line.substr(pos, comma - pos)));
        pos = comma + 1;
    }
    const std::string last = line.substr(pos);
    cols.push_back(trim_last ? trim(last) : last);
    return cols;
}

bool parse_nonnegative_int(const std::string& s, int& v) {
    if (s.empty()) return false;
    for (char c : s)
        if (!std::isdigit(static_cast<unsigned char>(c))) return false;
    v = std::atoi(s.c_str());
    return true;
}

}  // namespace

bool parse_ass_time(const std::string& s, long long& ms) {
    const std::size_t c1 = s.find(':');
    if (c1 == std::string::npos || c1 == 0) return false;
    const std::size_t c2 = s.find(':', c1 + 1);
    if (c2 == std::string::npos) return false;
    const std::size_t dot = s.find('.', c2 + 1);
    if (dot == std::string::npos) return false;
    const std::string sm = s.substr(c1 + 1, c2 - c1 - 1);
    const std::string ss = s.substr(c2 + 1, dot - c2 - 1);
    const std::string sc = s.substr(dot + 1);
    int h = 0, m = 0, sec = 0, cs = 0;
    if (!parse_nonnegative_int(s.substr(0, c1), h) || sm.size() != 2 ||
        !parse_nonnegative_int(sm, m) || ss.size() != 2 ||
        !parse_nonnegative_int(ss, sec) || sc.size() != 2 ||
        !parse_nonnegative_int(sc, cs))
        return false;
    if (m >= 60 || sec >= 60) return false;
    ms = ((static_cast<long long>(h) * 60 + m) * 60 + sec) * 100 + cs * 10;
    return true;
}

bool parse_ass(const std::string& data, Document& doc, std::string& err) {
    if (data.size() > kMaxInputBytes) {
        err = "input exceeds the 32 MiB limit";
        return false;
    }
    enum Section { kNone, kInfo, kStyles, kEvents };
    Section section = kNone;
    std::size_t pos = 0;
    if (data.compare(0, 3, "\xEF\xBB\xBF") == 0) pos = 3;  // UTF-8 BOM
    int line_no = 0;
    auto fail = [&](const std::string& msg) {
        err = "line " + std::to_string(line_no) + ": " + msg;
        return false;
    };
    while (pos <= data.size()) {
        const std::size_t nl = data.find('\n', pos);
        std::string line = (nl == std::string::npos) ? data.substr(pos)
                                                     : data.substr(pos, nl - pos);
        pos = (nl == std::string::npos) ? data.size() + 1 : nl + 1;
        if (!line.empty() && line.back() == '\r') line.pop_back();  // CRLF
        ++line_no;
        const std::string t = trim(line);
        if (t.empty() || t[0] == ';') continue;
        if (t.front() == '[' && t.back() == ']') {
            const std::string name = t.substr(1, t.size() - 2);
            if (name == "Script Info")
                section = kInfo;
            else if (name == "V4+ Styles" || name == "V4 Styles")
                section = kStyles;
            else if (name == "Events")
                section = kEvents;
            else
                section = kNone;
            continue;
        }
        const std::size_t colon = t.find(':');
        if (colon == std::string::npos) continue;
        const std::string key = t.substr(0, colon);
        const std::string rest = t.substr(colon + 1);
        if (section == kInfo) {
            int v = 0;
            if (key == "PlayResX" || key == "PlayResY") {
                if (!parse_nonnegative_int(trim(rest), v) || v <= 0)
                    return fail("invalid " + key + " '" + trim(rest) + "'");
                (key == "PlayResX" ? doc.play_res_x : doc.play_res_y) = v;
            }
        } else if (section == kStyles) {
            if (key == "Format") {
                doc.style_format = split_columns(rest, std::string::npos, true);
            } else if (key == "Style") {
                if (doc.style_format.empty())
                    return fail("Style line before any Format in [V4+ Styles]");
                const std::vector<std::string> cols =
                    split_columns(rest, doc.style_format.size(), false);
                if (cols.size() < doc.style_format.size())
                    return fail("Style has " + std::to_string(cols.size()) +
                                " columns, Format declares " +
                                std::to_string(doc.style_format.size()));
                Style st;
                st.line = line_no;
                for (std::size_t i = 0; i < doc.style_format.size(); ++i)
                    st.fields[doc.style_format[i]] = cols[i];
                st.name = st.fields["Name"];
                if (st.name.empty()) return fail("Style with empty Name");
                doc.styles.push_back(st);
            }
        } else if (section == kEvents) {
            if (key == "Format") {
                doc.event_format = split_columns(rest, std::string::npos, true);
            } else if (key == "Dialogue" || key == "Comment") {
                if (doc.event_format.empty())
                    return fail(key + " line before any Format in [Events]");
                const std::vector<std::string> cols =
                    split_columns(rest, doc.event_format.size(), false);
                if (cols.size() < doc.event_format.size())
                    return fail(key + " has " + std::to_string(cols.size()) +
                                " columns, Format declares " +
                                std::to_string(doc.event_format.size()));
                Event ev;
                ev.line = line_no;
                ev.kind = key;
                for (std::size_t i = 0; i < doc.event_format.size(); ++i)
                    ev.fields[doc.event_format[i]] = cols[i];
                for (const char* f : {"Start", "End", "Style", "Text"})
                    if (ev.fields.find(f) == ev.fields.end())
                        return fail(std::string("[Events] Format lacks '") + f + "'");
                ev.style = ev.fields["Style"];
                ev.text = ev.fields["Text"];
                if (!parse_ass_time(ev.fields["Start"], ev.start_ms))
                    return fail("invalid Start time '" + ev.fields["Start"] + "'");
                if (!parse_ass_time(ev.fields["End"], ev.end_ms))
                    return fail("invalid End time '" + ev.fields["End"] + "'");
                if (ev.end_ms < ev.start_ms)
                    return fail("negative duration (" + ev.fields["Start"] + " .. " +
                                ev.fields["End"] + ")");
                ev.id = static_cast<int>(doc.events.size()) + 1;
                doc.events.push_back(ev);
                if (doc.events.size() > kMaxEvents) {
                    err = "too many events (limit " + std::to_string(kMaxEvents) + ")";
                    return false;
                }
            }
        }
    }
    return true;
}

std::string generate_demo_ass() {
    // Three-level inheritance chain Base -> Mid -> Leaf (Parent extension,
    // "~" inherit markers) and six dialogues covering: recognized tags
    // (\b \i \fs \c \pos \an \move \fad \N \r \t -> 13 total), one unknown
    // tag (\xyzzy, kept) and one malformed unclosed brace.
    return
        "[Script Info]\n"
        "; generated by `subtitle-audit demo`\n"
        "ScriptType: v4.00+\n"
        "PlayResX: 1280\n"
        "PlayResY: 720\n"
        "ScaledBorderAndShadow: yes\n"
        "\n"
        "[V4+ Styles]\n"
        "Format: Name, Fontname, Fontsize, PrimaryColour, Bold, Italic, Alignment,"
        " MarginL, MarginR, MarginV, Outline, Parent\n"
        "Style: Base,Arial,40,&H00FFFFFF,0,0,2,60,60,50,2,\n"
        "Style: Mid,~,~,&H0000D7FF,~,~,~,~,~,~,~,Base\n"
        "Style: Leaf,~,56,~,1,~,~,~,~,~,~,Mid\n"
        "\n"
        "[Events]\n"
        "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"
        "Dialogue: 0,0:00:00.00,0:00:02.00,Base,,0,0,0,,Baseline style, reused across scenes\n"
        "Dialogue: 0,0:00:02.00,0:00:04.00,Mid,,0,0,0,,{\\b1\\i1\\fs52}Bold italic beat\n"
        "Dialogue: 0,0:00:04.00,0:00:06.00,Leaf,,0,0,0,,{\\c&H0000FF&\\pos(640,360)}Centered callout\n"
        "Dialogue: 0,0:00:06.00,0:00:08.00,Base,,0,0,0,,{\\an8\\move(120,120,560,120)\\fad(150,150)}Drifting HUD line\n"
        "Dialogue: 0,0:00:08.00,0:00:10.00,Mid,,0,0,0,,First line\\N{\\rLeaf}reset branch {\\xyzzy} kept\n"
        "Dialogue: 0,0:00:10.00,0:00:12.00,Leaf,,0,0,0,,{\\t(\\fs90)\\b1}Growing transform {unclosed\n";
}

}  // namespace subaudit
