#include "style_resolver.hpp"

#include <set>

#include "util.hpp"

namespace assaudit {
namespace {

std::map<std::string, std::string> g_defaults = {
    {"fontname", "Arial"},       {"fontsize", "18"},
    {"primarycolour", "&H00FFFFFF"}, {"secondarycolour", "&H000000FF"},
    {"outlinecolour", "&H00000000"}, {"backcolour", "&H80000000"},
    {"bold", "-1"},             {"italic", "0"},
    {"underline", "0"},         {"strikeout", "0"},
    {"scalex", "100"},          {"scaley", "100"},
    {"spacing", "0"},           {"angle", "0"},
    {"borderstyle", "1"},       {"outline", "2"},
    {"shadow", "2"},            {"alignment", "2"},
    {"marginl", "20"},          {"marginr", "20"},
    {"marginv", "20"},          {"encoding", "1"},
};

bool fieldValid(const std::string& key, const std::string& val,
                std::string& reason) {
  reason.clear();
  auto isNum = [&](double& d) { return parseDouble(val, d); };
  auto isIntRange = [&](int lo, int hi) {
    int v;
    if (!parseInt(val, v)) return false;
    return v >= lo && v <= hi;
  };
  if (key == "fontname") {
    if (trimCopy(val).empty()) reason = "empty font name";
    return reason.empty();
  }
  if (key == "fontsize") {
    double d;
    if (!isNum(d) || d <= 0 || d > 10000) reason = "font size out of range";
  } else if (key.find("colour") != std::string::npos) {
    if (!parseAssColor(val)) reason = "not an &HAABBGGRR& color";
  } else if (key == "bold" || key == "italic" || key == "underline" ||
             key == "strikeout") {
    int v;
    // ASS booleans: 0 = off, -1 = on/default, 1 = on (seen in real files).
    if (!parseInt(val, v) || !(v == 0 || v == 1 || v == -1))
      reason = "expected 0, 1 or -1";
  } else if (key == "scalex" || key == "scaley") {
    if (!isIntRange(1, 10000)) reason = "scale percentage out of range";
  } else if (key == "spacing" || key == "angle" || key == "outline" ||
             key == "shadow") {
    double d;
    if (!isNum(d) || d < 0 || d > 10000) reason = "value out of range";
  } else if (key == "borderstyle") {
    if (!isIntRange(1, 3)) reason = "border style must be 1..3";
  } else if (key == "alignment") {
    if (!isIntRange(1, 9)) reason = "alignment must be 1..9";
  } else if (key == "marginl" || key == "marginr" || key == "marginv" ||
             key == "encoding") {
    if (!isIntRange(0, 100000)) reason = "margin/encoding out of range";
  }
  return reason.empty();
}

StyleField defaultField(const std::string& key) {
  StyleField f;
  f.state = FieldState::Defaulted;
  f.source = "*default";
  f.source_line = 0;
  f.text = g_defaults[key];
  return f;
}

}  // namespace

std::map<std::string, std::string> styleDefaults() { return g_defaults; }

ResolveStats resolveStyles(AssDocument& doc) {
  ResolveStats stats;
  std::map<std::string, size_t> index;
  for (size_t i = 0; i < doc.styles.size(); ++i)
    index[doc.styles[i].name] = i;

  for (auto& style : doc.styles) {
    if (style.duplicate) {
      style.blocked = true;
      style.errors.push_back(
          {style.line, "style_duplicate",
           "duplicate style name; definitions shadow each other", true});
    }
    if (!style.parent.empty()) stats.inheritance_edges++;

    // Walk parent pointers with an on-path set to detect cycles.
    std::vector<std::string> chain;
    std::set<std::string> onPath;
    const AssStyle* cur = &style;
    bool blocked = style.duplicate;
    while (true) {
      if (onPath.count(cur->name)) {
        style.errors.push_back(
            {cur->parent_line, "inheritance_cycle",
             "inheritance cycle through '" + cur->name + "'", true});
        blocked = true;
        break;
      }
      chain.push_back(cur->name);
      onPath.insert(cur->name);
      if (cur->parent.empty()) break;
      auto it = index.find(cur->parent);
      if (it == index.end()) {
        style.errors.push_back(
            {cur->parent_line, "parent_missing",
             "parent style '" + cur->parent + "' does not exist", true});
        blocked = true;
        break;
      }
      cur = &doc.styles[it->second];
    }
    style.chain = chain;
    style.blocked = blocked;
    if (blocked) stats.inheritance_errors++;
    // Fold fields: earliest style on the child-first chain wins.
    std::set<std::string> taken;
    for (const auto& name : chain) {
      const AssStyle& node = doc.styles[index[name]];
      for (const auto& [key, field] : node.own) {
        if (taken.count(key)) continue;
        // Explicit empties are recorded in `own` but do not supply a
        // value: the search continues down the chain and into defaults.
        if (field.state == FieldState::ExplicitEmpty) continue;
        std::string reason;
        if (!fieldValid(key, field.text, reason)) {
          style.errors.push_back(
              {field.source_line, "field_invalid",
               "style '" + name + "' field " + key + ": " + reason +
                   " (skipped)"});
          continue;
        }
        StyleField merged = field;
        merged.source = name;
        style.merged[key] = merged;
        taken.insert(key);
      }
    }
    for (const auto& [key, def] : g_defaults) {
      if (!taken.count(key)) style.merged[key] = defaultField(key);
    }
  }
  return stats;
}

}  // namespace assaudit
