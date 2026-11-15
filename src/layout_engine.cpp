#include "layout_engine.hpp"

#include <set>
#include <sstream>

#include "style_resolver.hpp"
#include "tag_resolver.hpp"
#include "util.hpp"

namespace assaudit {
namespace {

// Canonical V4+ Style column order; Parent is our extension in last
// position so libass ignores the unknown trailing column.
const char* kStyleOrder[] = {
    "fontname",      "fontsize",       "primarycolour", "secondarycolour",
    "outlinecolour", "backcolour",     "bold",          "italic",
    "underline",     "strikeout",      "scalex",        "scaley",
    "spacing",       "angle",          "borderstyle",   "outline",
    "shadow",        "alignment",      "marginl",       "marginr",
    "marginv",       "encoding"};

std::string mergedValue(const AssStyle& st, const std::string& key,
                        const std::map<std::string, std::string>& defaults) {
  auto it = st.merged.find(key);
  if (it != st.merged.end() && it->second.state != FieldState::ExplicitEmpty)
    return it->second.text;
  auto d = defaults.find(key);
  return d == defaults.end() ? "" : d->second;
}

// Build the shared header once: every usable style is emitted with its
// fully merged values so that \r OtherStyle inside an event can switch
// to any style, not just the event's own inheritance chain.
std::string synthesizeHeader(const AssDocument& doc, int width, int height) {
  auto defaults = styleDefaults();
  std::ostringstream o;
  o << "[Script Info]\nScriptType: v4.00+\nPlayResX: " << width
    << "\nPlayResY: " << height << "\nWrapStyle: " << doc.wrap_style
    << "\nScaledBorderAndShadow: " << (doc.scaled_border ? "yes" : "no")
    << "\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, "
       "SecondaryColour, OutlineColour, BackColour, Bold, Italic, "
       "Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, "
       "Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n";
  bool haveDefault = false;
  for (const auto& st : doc.styles) {
    if (st.blocked) continue;
    if (st.name == "Default") haveDefault = true;
    o << "Style: " << st.name;
    for (const char* k : kStyleOrder)
      o << "," << mergedValue(st, k, defaults);
    o << "\n";
  }
  if (!haveDefault) {
    o << "Style: Default";
    for (const char* k : kStyleOrder) o << "," << defaults.at(k);
    o << "\n";
  }
  o << "\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, "
       "MarginV, Effect, Text\n";
  return o.str();
}

std::string synthesizeEventAss(const std::string& header,
                               const std::string& styleName,
                               const AssEvent& ev) {
  std::ostringstream o;
  o << header;
  o << "Dialogue: 0,0:00:00.00,0:00:10.00,"
    << (styleName.empty() ? "Default" : styleName) << ",,0,0,0,,,"
    << ev.text << "\n";
  return o.str();
}

std::string jsonDiag(const std::vector<Diagnostic>& ds) {
  std::ostringstream o;
  o << "[";
  for (size_t i = 0; i < ds.size(); ++i) {
    if (i) o << ",";
    o << "{\"line\":" << ds[i].line << ",\"code\":\""
      << jsonEscape(ds[i].code) << "\",\"message\":\""
      << jsonEscape(ds[i].message) << "\"}";
  }
  o << "]";
  return o.str();
}

}  // namespace

LayoutOutcome runLayout(const AssDocument& doc, AssRenderer& renderer,
                        const LayoutConfig& cfg) {
  LayoutOutcome out;
  std::map<std::string, const AssStyle*> byName;
  for (const auto& st : doc.styles) byName[st.name] = &st;
  const std::string header = synthesizeHeader(doc, cfg.width, cfg.height);

  for (const auto& ev : doc.events) {
    if (ev.is_comment) continue;
    EventReport rep;
    rep.event_id = ev.id;
    rep.source_line = ev.line;
    rep.style = ev.style_name;
    bool blocked = false;

    for (const auto& d : ev.diagnostics) rep.diagnostics.push_back(d);
    if (ev.start_ms < 0 || ev.end_ms < 0 || ev.end_ms < ev.start_ms ||
        ev.end_ms == ev.start_ms)
      blocked = true;

    const AssStyle* style = nullptr;
    auto it = byName.find(ev.style_name);
    if (it == byName.end()) {
      Diagnostic d{ev.line, "style_missing",
                   "event references unknown style '" + ev.style_name + "'"};
      rep.diagnostics.push_back(d);
    } else {
      style = it->second;
      if (style->blocked) {
        blocked = true;
        for (const auto& d : style->errors) rep.diagnostics.push_back(d);
      }
    }

    TagReport tags = resolveTags(ev.text);
    rep.tag_total = tags.total;
    rep.tag_valid = tags.valid;
    rep.tag_unknown = tags.unknown;
    rep.tag_override = tags.overrides;
    for (const auto& w : tags.warnings) rep.diagnostics.push_back(w);

    if (tags.has_pos && (tags.pos_x < 0 || tags.pos_y < 0 ||
                         tags.pos_x > cfg.width || tags.pos_y > cfg.height)) {
      rep.diagnostics.push_back(
          {ev.line, "out_of_resolution",
           "\\pos (" + std::to_string(tags.pos_x) + "," +
               std::to_string(tags.pos_y) + ") outside " +
               std::to_string(cfg.width) + "x" +
               std::to_string(cfg.height)});
    }
    if (tags.has_move) {
      double xs[2] = {tags.move_x1, tags.move_x2};
      double ys[2] = {tags.move_y1, tags.move_y2};
      for (double x : xs)
        for (double y : ys)
          if (x < 0 || y < 0 || x > cfg.width || y > cfg.height)
            rep.diagnostics.push_back(
                {ev.line, "out_of_resolution",
                 "\\move endpoint outside configured resolution"});
    }

    rep.blocked = blocked;
    if (blocked) {
      out.blocked_events++;
      if (cfg.strict) out.structural_error = true;
    } else {
      std::string ass = synthesizeEventAss(header, ev.style_name, ev);
      RenderResult rr;
      std::string err = renderer.renderTrack(ass, 0, rr);
      if (!err.empty()) {
        rep.diagnostics.push_back({ev.line, "render_failed", err});
        out.render_failure = true;
      } else {
        rep.rendered = true;
        rep.region_width = rr.region_width;
        rep.region_height = rr.region_height;
        rep.image_blocks = rr.image_blocks;
        rep.pixel_digest = rr.pixel_digest;
        rep.font_fallback = rr.font_fallback;
        if (rr.zero_frames)
          rep.diagnostics.push_back(
              {ev.line, "render_empty", "libass produced no image frames"});
        out.rendered++;
      }
    }
    out.reports.push_back(rep);

    std::ostringstream j;
    j << "{\"event_id\":" << rep.event_id << ",\"source_line\":"
      << rep.source_line << ",\"style\":\"" << jsonEscape(rep.style)
      << "\",\"tag_total\":" << rep.tag_total
      << ",\"tag_valid\":" << rep.tag_valid
      << ",\"tag_unknown\":" << rep.tag_unknown
      << ",\"tag_override\":" << rep.tag_override
      << ",\"rendered\":" << (rep.rendered ? "true" : "false")
      << ",\"blocked\":" << (rep.blocked ? "true" : "false")
      << ",\"region_width\":" << rep.region_width
      << ",\"region_height\":" << rep.region_height
      << ",\"image_blocks\":" << rep.image_blocks
      << ",\"font_fallback\":" << (rep.font_fallback ? "true" : "false")
      << ",\"pixel_digest\":\"" << rep.pixel_digest << "\""
      << ",\"diagnostics\":" << jsonDiag(rep.diagnostics) << "}";
    out.jsonl.push_back(j.str());
  }
  for (const auto& d : doc.diagnostics)
    if (d.fatal) out.structural_error = true;
  return out;
}

}  // namespace assaudit
