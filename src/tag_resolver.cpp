#include "tag_resolver.hpp"

#include <cctype>
#include <set>

#include "util.hpp"

namespace assaudit {
namespace {

bool isNameChar(char c) {
  return std::isalpha(static_cast<unsigned char>(c)) || c == '1';
}

const std::set<std::string>& overrideTags() {
  static const std::set<std::string> tags = {
      "b",   "i",   "u",    "s",     "fn", "fs", "c",   "1c", "2c",
      "3c",  "4c",  "alpha", "1a",   "2a", "3a", "4a",  "fscx", "fscy",
      "fsp", "frx", "fry",  "frz",   "fr", "bord", "xbord", "ybord",
      "shad", "xshad", "yshad", "be", "blur", "k", "kf", "ko", "q",
      "an",  "a",   "pos",  "move",  "org", "fad", "fade", "t", "p",
  };
  return tags;
}

const std::set<std::string>& knownTags() {
  static const std::set<std::string> tags = [] {
    auto t = overrideTags();
    t.insert({"r", "n", "N", "h", "pbo"});
    return t;
  }();
  return tags;
}

bool validateParam(const std::string& name, const std::string& p) {
  auto numeric = [&]() {
    double d;
    return parseDouble(p, d);
  };
  if (name == "fn") return !trimCopy(p).empty();
  if (name == "r" || name == "h") return true;
  if (name == "n" || name == "N") return p.empty();
  if (name == "1c" || name == "2c" || name == "3c" || name == "4c" ||
      name == "c")
    return parseAssColor(p).has_value();
  if (name == "alpha" || name == "1a" || name == "2a" || name == "3a" ||
      name == "4a") {
    double d;
    return parseDouble(p, d) && d >= 0 && d <= 255;
  }
  if (name == "an") {
    int v;
    return parseInt(p, v) && v >= 1 && v <= 9;
  }
  if (name == "a") {
    int v;
    return parseInt(p, v) && v >= 1 && v <= 11;
  }
  if (name == "pos") {
    auto xy = splitFields(p);
    double x, y;
    return xy.size() == 2 && parseDouble(xy[0], x) && parseDouble(xy[1], y);
  }
  if (name == "move") {
    auto xy = splitFields(p);
    double d;
    return (xy.size() == 4 || xy.size() == 6) &&
           std::all_of(xy.begin(), xy.end(),
                       [&](const std::string& v) { return parseDouble(v, d); });
  }
  if (name == "t") return true;  // contents validated as nested commands
  return numeric();
}
// Parse one command starting at text[i] (i points just after the slash).
// Returns the command; advances i past it. Parens are balanced greedily.
TagCommand nextCommand(const std::string& text, size_t& i, bool inTransform,
                       std::vector<Diagnostic>& warnings, int lineHint) {
  TagCommand cmd;
  cmd.in_transform = inTransform;
  size_t start = i;
  while (i < text.size() && isNameChar(text[i])) ++i;
  cmd.name = toLower(text.substr(start, i - start));
  if (cmd.name.empty()) {
    cmd.valid = false;
    cmd.unknown = true;
    // consume one token of junk so scanning always makes progress
    if (i < text.size()) ++i;
    return cmd;
  }
  if (i < text.size() && text[i] == '(') {
    int depth = 0;
    size_t p0 = ++i;
    while (i < text.size()) {
      if (text[i] == '(') ++depth;
      if (text[i] == ')') {
        if (depth == 0) break;
        --depth;
      }
      ++i;
    }
    if (i >= text.size()) {
      warnings.push_back({lineHint, "tag_unbalanced",
                           "unbalanced parenthesis in '\\" + cmd.name + "'"});
      cmd.param = text.substr(p0);
    } else {
      cmd.param = text.substr(p0, i - p0);
      ++i;  // consume ')'
    }
  }
  if (knownTags().count(cmd.name) == 0) {
    cmd.unknown = true;
    cmd.valid = false;
  } else if (!validateParam(cmd.name, cmd.param)) {
    cmd.valid = false;
    warnings.push_back({lineHint, "tag_bad_param",
                         "invalid parameter for '\\" + cmd.name + "': " +
                             cmd.param});
  }
  return cmd;
}

}  // namespace

TagReport resolveTags(const std::string& text) {
  TagReport rep;
  size_t i = 0;
  bool transformActive = false;
  while (i < text.size()) {
    if (text[i] != '{') {
      ++i;
      continue;
    }
    ++i;  // consume '{'
    size_t groupStart = i;
    bool closed = false;
    while (i < text.size()) {
      if (text[i] == '{') {
        rep.nested_groups++;  // new nested group starts here
        break;
      }
      if (text[i] == '}') {
        closed = true;
        break;
      }
      ++i;
    }
    size_t groupEnd = i;  // exclusive content end (or text.size())
    if (!closed && i < text.size() && text[i] == '{') {
      // restart at the nested group; current group had no closing brace
      rep.unclosed_groups++;
      rep.warnings.push_back(
          {0, "tag_unclosed",
           "'{' group closed by another '{' instead of '}'"});
      continue;
    }
    if (!closed) {
      rep.unclosed_groups++;
      rep.warnings.push_back(
          {0, "tag_unclosed", "unclosed '{' group; consumed to end of line"});
    } else {
      ++i;  // consume '}'
    }
    size_t j = groupStart;
    while (j < groupEnd) {
      char c = text[j];
      if (c == '\\') {
        ++j;
        TagCommand cmd = nextCommand(text, j, transformActive, rep.warnings, 0);
        if (cmd.name == "t" && cmd.valid) {
          transformActive = true;
          // Nested transform commands: parse the last parenthesised block.
          auto pp = cmd.param.rfind('(');
          std::string inner = pp == std::string::npos
                                  ? cmd.param
                                  : cmd.param.substr(pp + 1);
          size_t k = 0;
          while (k < inner.size()) {
            if (inner[k] != '\\') { ++k; continue; }
            ++k;
            TagCommand sub = nextCommand(inner, k, true, rep.warnings, 0);
            rep.total++;
            if (sub.unknown) rep.unknown++;
            if (sub.valid) {
              rep.valid++;
              if (overrideTags().count(sub.name)) rep.overrides++;
            }
            rep.commands.push_back(sub);
          }
        }
        rep.total++;
        if (cmd.unknown) rep.unknown++;
        if (cmd.valid) {
          rep.valid++;
          if (overrideTags().count(cmd.name)) rep.overrides++;
          double x1, y1, x2, y2;
          if (cmd.name == "pos") {
            auto xy = splitFields(cmd.param);
            if (xy.size() == 2 && parseDouble(xy[0], x1) &&
                parseDouble(xy[1], y1)) {
              rep.has_pos = true;
              rep.pos_x = x1;
              rep.pos_y = y1;
            }
          } else if (cmd.name == "move") {
            auto xy = splitFields(cmd.param);
            if (xy.size() >= 4 && parseDouble(xy[0], x1) &&
                parseDouble(xy[1], y1) && parseDouble(xy[2], x2) &&
                parseDouble(xy[3], y2)) {
              rep.has_move = true;
              rep.move_x1 = x1;
              rep.move_y1 = y1;
              rep.move_x2 = x2;
              rep.move_y2 = y2;
            }
          }
        }
        rep.commands.push_back(cmd);
        transformActive = false;  // scope is the single \t command
      } else {
        ++j;
      }
    }
  }
  return rep;
}

}  // namespace assaudit
