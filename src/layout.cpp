#include "layout.hpp"

#include <cstdio>
#include <map>

namespace subaudit {
namespace {

// Standard v4+ style columns emitted into the normalized documents.
const std::vector<std::string>& std_fields() {
    static const std::vector<std::string> fields = {
        "Name",        "Fontname", "Fontsize",   "PrimaryColour", "SecondaryColour",
        "OutlineColour", "BackColour", "Bold",   "Italic",        "Underline",
        "StrikeOut",   "ScaleX",   "ScaleY",     "Spacing",       "Angle",
        "BorderStyle", "Outline",  "Shadow",     "Alignment",     "MarginL",
        "MarginR",     "MarginV",  "Encoding"};
    return fields;
}

const std::map<std::string, std::string>& field_defaults() {
    static const std::map<std::string, std::string> defaults = {
        {"Fontname", "Arial"},        {"Fontsize", "48"},
        {"PrimaryColour", "&H00FFFFFF"}, {"SecondaryColour", "&H000000FF"},
        {"OutlineColour", "&H00000000"}, {"BackColour", "&H80000000"},
        {"Bold", "0"},                {"Italic", "0"},
        {"Underline", "0"},           {"StrikeOut", "0"},
        {"ScaleX", "100"},            {"ScaleY", "100"},
        {"Spacing", "0"},             {"Angle", "0"},
        {"BorderStyle", "1"},         {"Outline", "2"},
        {"Shadow", "0"},              {"Alignment", "2"},
        {"MarginL", "10"},            {"MarginR", "10"},
        {"MarginV", "10"},            {"Encoding", "1"}};
    return defaults;
}

std::string style_field(const ResolvedStyle& st, const std::string& key) {
    const auto it = st.fields.find(key);
    if (it != st.fields.end() && !it->second.empty()) return it->second;
    const auto d = field_defaults().find(key);
    return d != field_defaults().end() ? d->second : "0";
}

std::string event_field(const Event& ev, const char* key, const char* dflt) {
    const auto it = ev.fields.find(key);
    return it != ev.fields.end() && !it->second.empty() ? it->second : dflt;
}

}  // namespace

std::string format_ass_time(long long ms) {
    const long long h = ms / 3600000;
    const long long m = (ms / 60000) % 60;
    const long long s = (ms / 1000) % 60;
    const long long cs = (ms % 1000) / 10;
    char buf[32];
    std::snprintf(buf, sizeof buf, "%lld:%02lld:%02lld.%02lld", h, m, s, cs);
    return buf;
}

bool build_layout_inputs(const Document& doc, const std::vector<ResolvedStyle>& styles,
                         std::vector<LayoutInput>& out, std::string& err) {
    // The style section is identical for every event; build it once with
    // inheritance flattened and defaults filled.
    std::string style_block = "Format: ";
    for (std::size_t i = 0; i < std_fields().size(); ++i) {
        if (i) style_block += ", ";
        style_block += std_fields()[i];
    }
    style_block += "\n";
    std::map<std::string, bool> known;
    for (const ResolvedStyle& st : styles) {
        known[st.name] = true;
        style_block += "Style: " + st.name;
        for (std::size_t i = 1; i < std_fields().size(); ++i)
            style_block += "," + style_field(st, std_fields()[i]);
        style_block += "\n";
    }

    for (const Event& ev : doc.events) {
        if (ev.kind != "Dialogue") continue;
        if (!known.count(ev.style)) {
            err = "line " + std::to_string(ev.line) + ": event " +
                  std::to_string(ev.id) + " uses unknown style '" + ev.style + "'";
            return false;
        }
        LayoutInput li;
        li.event_id = ev.id;
        li.start_ms = ev.start_ms;
        li.duration_ms = ev.end_ms - ev.start_ms;
        li.mid_ms = li.duration_ms / 2;
        std::string a = "[Script Info]\nScriptType: v4.00+\n";
        a += "PlayResX: " + std::to_string(doc.play_res_x) + "\n";
        a += "PlayResY: " + std::to_string(doc.play_res_y) + "\n";
        a += "ScaledBorderAndShadow: yes\n\n[V4+ Styles]\n" + style_block;
        a += "\n[Events]\n"
             "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV,"
             " Effect, Text\n";
        a += "Dialogue: " + event_field(ev, "Layer", "0") + ",0:00:00.00," +
             format_ass_time(li.duration_ms) + "," + ev.style + "," +
             event_field(ev, "Name", "") + "," + event_field(ev, "MarginL", "0") + "," +
             event_field(ev, "MarginR", "0") + "," + event_field(ev, "MarginV", "0") +
             "," + event_field(ev, "Effect", "") + "," + ev.text + "\n";
        li.ass = std::move(a);
        out.push_back(std::move(li));
    }
    return true;
}

}  // namespace subaudit
