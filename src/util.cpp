#include "util.hpp"

#include <algorithm>
#include <cctype>
#include <cmath>
#include <sstream>

namespace assaudit {

std::string trimCopy(std::string s) {
  auto notSpace = [](unsigned char c) { return !std::isspace(c); };
  s.erase(s.begin(), std::find_if(s.begin(), s.end(), notSpace));
  s.erase(std::find_if(s.rbegin(), s.rend(), notSpace).base(), s.end());
  return s;
}

std::string toLower(std::string s) {
  std::transform(s.begin(), s.end(), s.begin(),
                 [](unsigned char c) { return std::tolower(c); });
  return s;
}

bool iequals(const std::string& a, const std::string& b) {
  if (a.size() != b.size()) return false;
  for (size_t i = 0; i < a.size(); ++i)
    if (std::tolower(static_cast<unsigned char>(a[i])) !=
        std::tolower(static_cast<unsigned char>(b[i])))
      return false;
  return true;
}

std::vector<std::string> splitFields(const std::string& s, int maxParts) {
  std::vector<std::string> out;
  std::string cur;
  int depth = 0;
  for (size_t i = 0; i < s.size(); ++i) {
    char c = s[i];
    if (c == '{') ++depth;
    if (c == '}' && depth > 0) --depth;
    if (c == ',' && depth == 0 &&
        (maxParts <= 0 || static_cast<int>(out.size()) < maxParts - 1)) {
      out.push_back(cur);
      cur.clear();
    } else {
      cur.push_back(c);
    }
  }
  out.push_back(cur);
  return out;
}

std::optional<long long> parseTimecode(const std::string& in) {
  std::string s = trimCopy(in);
  // H:MM:SS.cc (hours may exceed one digit); reject junk deterministically.
  if (s.empty() ||
      s.find_first_not_of("0123456789:.") != std::string::npos)
    return std::nullopt;
  auto digits = [](const std::string& t) {
    if (t.empty()) return false;
    for (char c : t)
      if (!std::isdigit(static_cast<unsigned char>(c))) return false;
    return true;
  };
  std::vector<long long> parts;
  std::string token;
  for (char c : s) {
    if (c == ':') {
      if (!digits(token)) return std::nullopt;
      parts.push_back(std::stoll(token));
      token.clear();
    } else {
      token.push_back(c);
    }
  }
  if (token.empty()) return std::nullopt;
  auto dot = token.find('.');
  long long sec = 0, cs = 0;
  if (dot == std::string::npos) {
    if (!digits(token)) return std::nullopt;
    sec = std::stoll(token);
  } else {
    std::string whole = token.substr(0, dot);
    auto frac = token.substr(dot + 1);
    if (!digits(whole) || frac.empty() || frac.size() > 2 ||
        frac.find('.') != std::string::npos)
      return std::nullopt;
    sec = std::stoll(whole);
    while (frac.size() < 2) frac.push_back('0');
    cs = std::stoll(frac.substr(0, 2));
  }
  parts.push_back(sec);
  if (parts.size() < 2 || parts.size() > 3) return std::nullopt;
  long long min = parts[parts.size() - 2];
  long long hour = parts.size() == 3 ? parts[0] : 0;
  if (min >= 60 || cs >= 100) return std::nullopt;
  return ((hour * 60 + min) * 60 + sec) * 100 + cs;
}

bool parseDouble(const std::string& s, double& out) {
  std::string t = trimCopy(s);
  if (t.empty()) return false;
  size_t used = 0;
  try {
    out = std::stod(t, &used);
  } catch (...) {
    return false;
  }
  return used == t.size() && std::isfinite(out);
}

bool parseInt(const std::string& s, int& out) {
  double d = 0;
  if (!parseDouble(s, d) || d != std::floor(d)) return false;
  if (d < -2147483648.0 || d > 2147483647.0) return false;
  out = static_cast<int>(d);
  return true;
}

std::optional<AssColor> parseAssColor(const std::string& in) {
  std::string s = trimCopy(in);
  if (s.size() < 2 || s.front() != '&') return std::nullopt;
  s = s.substr(1);
  if (!s.empty() && (s.front() == 'H' || s.front() == 'h')) s.erase(0, 1);
  if (!s.empty() && s.back() == '&') s.pop_back();
  if (s.empty() || s.find_first_not_of("0123456789ABCDEFabcdef") !=
                       std::string::npos)
    return std::nullopt;
  unsigned long v = std::stoul(s, nullptr, 16);
  AssColor c;
  c.b = (v >> 16) & 0xFF;
  c.g = (v >> 8) & 0xFF;
  c.r = v & 0xFF;
  c.a = s.size() >= 8 ? ((v >> 24) & 0xFF) : 0;
  c.defined = true;
  return c;
}

std::string colorToAss(int a, int r, int g, int b) {
  char buf[16];
  std::snprintf(buf, sizeof(buf), "&H%02X%02X%02X%02X&", a, b, g, r);
  return buf;
}

uint64_t fnv1a64(const uint8_t* data, size_t len, uint64_t h) {
  for (size_t i = 0; i < len; ++i) {
    h ^= data[i];
    h *= 1099511628211ULL;
  }
  return h;
}

std::string hex64(uint64_t v) {
  static const char* d = "0123456789abcdef";
  std::string out(16, '0');
  for (int i = 15; i >= 0; --i) {
    out[static_cast<size_t>(i)] = d[v & 0xF];
    v >>= 4;
  }
  return out;
}

std::string jsonEscape(const std::string& s) {
  std::ostringstream o;
  for (char raw : s) {
    unsigned char c = static_cast<unsigned char>(raw);
    switch (c) {
      case '"': o << "\\\""; break;
      case '\\': o << "\\\\"; break;
      case '\n': o << "\\n"; break;
      case '\r': o << "\\r"; break;
      case '\t': o << "\\t"; break;
      default:
        if (c < 0x20) {
          char b[8];
          std::snprintf(b, sizeof(b), "\\u%04x", c);
          o << b;
        } else {
          o << static_cast<char>(c);
        }
    }
  }
  return o.str();
}

}  // namespace assaudit
