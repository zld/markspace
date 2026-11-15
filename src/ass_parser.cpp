#include "ass_parser.hpp"

#include <sstream>

#include "util.hpp"

namespace assaudit {
namespace {

std::string sectionOf(const std::string& line) {
  std::string t = trimCopy(line);
  if (t.size() >= 2 && t.front() == '[' && t.back() == ']')
    return toLower(t.substr(1, t.size() - 2));
  return "";
}

void warn(AssDocument& d, int line, std::string code, std::string msg,
          bool fatal = false) {
  d.diagnostics.push_back({line, std::move(code), std::move(msg), fatal});
}

std::map<std::string, StyleField> buildStyleOwn(
    const std::vector<std::string>& names,
    const std::vector<std::string>& values, int lineNo) {
  static const char* kFields[] = {
      "fontname",      "fontsize",       "primarycolour", "secondarycolour",
      "outlinecolour", "backcolour",     "bold",          "italic",
      "underline",     "strikeout",      "scalex",        "scaley",
      "spacing",       "angle",          "borderstyle",   "outline",
      "shadow",        "alignment",      "marginl",       "marginr",
      "marginv",       "encoding"};
  std::map<std::string, StyleField> own;
  for (size_t i = 0; i < names.size() && i < values.size(); ++i) {
    const std::string& key = names[i];
    bool known = false;
    for (const char* f : kFields)
      if (key == f) known = true;
    if (!known) continue;
    StyleField sf;
    sf.source_line = lineNo;
    if (values[i].empty()) {
      sf.state = FieldState::ExplicitEmpty;
    } else {
      sf.state = FieldState::Explicit;
      sf.text = values[i];
    }
    own[key] = sf;
  }
  return own;
}

void handleStyle(AssDocument& d, const std::vector<std::string>& names,
                 const std::string& rest, int lineNo) {
  auto values = splitFields(rest, static_cast<int>(names.size()));
  if (values.size() != names.size()) {
    warn(d, lineNo, "style_arity",
         "Style record column count does not match Format", true);
    return;
  }
  AssStyle st;
  st.line = lineNo;
  for (size_t i = 0; i < names.size(); ++i) {
    if (names[i] == "name")
      st.name = trimCopy(values[i]);
    else if (names[i] == "parent")
      st.parent = trimCopy(values[i]);
  }
  if (st.name.empty()) {
    warn(d, lineNo, "style_name_empty", "Style has an empty name", true);
    return;
  }
  st.parent_line = lineNo;
  st.own = buildStyleOwn(names, values, lineNo);
  for (auto& other : d.styles) {
    if (other.name == st.name) {
      other.duplicate = true;
      st.duplicate = true;
      warn(d, lineNo, "style_duplicate",
           "duplicate style name '" + st.name + "' (first at line " +
               std::to_string(other.line) + ")",
           true);
    }
  }
  d.styles.push_back(std::move(st));
}

void handleEvent(AssDocument& d, bool comment,
                 const std::vector<std::string>& names,
                 const std::string& rest, int lineNo, int& eventSeq) {
  auto values = splitFields(rest, static_cast<int>(names.size()));
  if (values.size() != names.size()) {
    warn(d, lineNo, "event_arity",
         "Event record column count does not match Format", true);
    return;
  }
  AssEvent ev;
  ev.line = lineNo;
  ev.is_comment = comment;
  for (size_t i = 0; i < names.size(); ++i)
    ev.fields[names[i]] = values[i];
  auto get = [&](const char* k) -> std::string {
    auto it = ev.fields.find(k);
    return it == ev.fields.end() ? "" : it->second;
  };
  parseInt(trimCopy(get("layer")), ev.layer);
  ev.style_name = trimCopy(get("style"));
  ev.text = get("text");
  auto s = parseTimecode(get("start"));
  auto e = parseTimecode(get("end"));
  if (!s) {
    ev.diagnostics.push_back({lineNo, "timecode_bad",
                              "invalid Start time '" + get("start") + "'"});
  } else {
    ev.start_ms = *s;
  }
  if (!e) {
    ev.diagnostics.push_back({lineNo, "timecode_bad",
                              "invalid End time '" + get("end") + "'"});
  } else {
    ev.end_ms = *e;
  }
  if (s && e && *e < *s)
    ev.diagnostics.push_back(
        {lineNo, "timecode_order", "event ends before it starts"});
  if (s && e && *e == *s)
    ev.diagnostics.push_back(
        {lineNo, "zero_duration", "event has zero duration"});
  if (!comment) ev.id = ++eventSeq;
  d.events.push_back(std::move(ev));
}

void handleScriptInfo(AssDocument& d, const std::string& line,
                      int /*lineNo*/) {
  auto pos = line.find(':');
  if (pos == std::string::npos) return;
  std::string key = toLower(trimCopy(line.substr(0, pos)));
  std::string val = trimCopy(line.substr(pos + 1));
  int iv = 0;
  if ((key == "playresx" || key == "playresy") && parseInt(val, iv) &&
      iv > 0) {
    if (key == "playresx") d.play_res_x = iv;
    else d.play_res_y = iv;
  } else if (key == "wrapstyle" && parseInt(val, iv) && iv >= 0 && iv <= 3) {
    d.wrap_style = iv;
  } else if (key == "scaledborderandshadow") {
    d.scaled_border = iequals(val, "yes") || val == "1";
  }
}

}  // namespace

bool parseAss(const std::string& content, const std::string& sourceLabel,
              AssDocument& doc) {
  doc = AssDocument{};
  doc.source_label = sourceLabel;
  std::istringstream in(content);
  std::string raw;
  int lineNo = 0, eventSeq = 0;
  std::string section;
  std::vector<std::string> styleFmt, eventFmt;
  while (std::getline(in, raw)) {
    ++lineNo;
    if (!raw.empty() && raw.back() == '\r') raw.pop_back();
    if (lineNo == 1 && raw.size() >= 3 &&
        static_cast<unsigned char>(raw[0]) == 0xEF &&
        static_cast<unsigned char>(raw[1]) == 0xBB &&
        static_cast<unsigned char>(raw[2]) == 0xBF) {
      raw.erase(0, 3);  // consume UTF-8 BOM
    }
    std::string line = raw;
    std::string t = trimCopy(line);
    if (t.empty()) continue;
    std::string hdr = sectionOf(line);
    if (!hdr.empty()) {
      section = hdr;
      continue;
    }
    if (t.front() == ';') continue;  // full-line comment
    if (section == "script info") {
      handleScriptInfo(doc, line, lineNo);
      continue;
    }
    auto colon = line.find(':');
    if (colon == std::string::npos) continue;
    std::string type = trimCopy(line.substr(0, colon));
    std::string rest = line.substr(colon + 1);
    if (section == "v4+ styles" || section == "v4 styles") {
      if (iequals(type, "Format")) {
        styleFmt.clear();
        for (auto& f : splitFields(rest)) styleFmt.push_back(toLower(trimCopy(f)));
      } else if (iequals(type, "Style")) {
        if (styleFmt.empty())
          warn(doc, lineNo, "format_missing",
               "Style record before any Format line", true);
        else
          handleStyle(doc, styleFmt, rest, lineNo);
      }
    } else if (section == "events") {
      if (iequals(type, "Format")) {
        eventFmt.clear();
        for (auto& f : splitFields(rest)) eventFmt.push_back(toLower(trimCopy(f)));
      } else if (iequals(type, "Dialogue") || iequals(type, "Comment")) {
        if (eventFmt.empty())
          warn(doc, lineNo, "format_missing",
               "Event record before any Format line", true);
        else
          handleEvent(doc, iequals(type, "Comment"), eventFmt, rest, lineNo,
                      eventSeq);
      }
    }
  }
  if (section.empty() && doc.styles.empty() && doc.events.empty())
    warn(doc, lineNo, "empty_document", "no ASS sections found", true);
  return true;
}

}  // namespace assaudit
