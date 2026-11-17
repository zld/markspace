#include "manifest.hpp"

#include "frame_buffer.hpp"  // kMaxSide

#include <cctype>
#include <cstdio>
#include <sstream>
#include <stdexcept>

namespace tl {

static int64_t gcd64(int64_t a, int64_t b) {
  if (a < 0) a = -a;
  if (b < 0) b = -b;
  while (b != 0) {
    int64_t t = a % b;
    a = b;
    b = t;
  }
  return a;
}

Rational make_rational(int64_t num, int64_t den) {
  if (den == 0) throw std::invalid_argument("rational with zero denominator");
  if (den < 0) {
    num = -num;
    den = -den;
  }
  int64_t g = gcd64(num, den);
  if (g == 0) g = 1;
  return {num / g, den / g};
}

const char* to_string(GenType t) {
  switch (t) {
    case GenType::Gradient: return "gradient";
    case GenType::Checker: return "checker";
    case GenType::Shapes: return "shapes";
  }
  return "?";
}

const char* to_string(KernelType t) {
  switch (t) {
    case KernelType::Crossfade: return "crossfade";
    case KernelType::Wipe: return "wipe";
    case KernelType::Radial: return "radial";
  }
  return "?";
}

namespace {

struct Parser {
  const std::string& text;
  Manifest& m;
  std::string& err;
  int line_no = 0;
  bool have_canvas = false;
  bool have_fps = false;
  bool have_clip = false;  // a clip is open for `speed` lines

  bool fail(const std::string& msg) {
    char buf[64];
    std::snprintf(buf, sizeof buf, "line %d: ", line_no);
    err = std::string(buf) + msg;
    return false;
  }

  static bool parse_int(const std::string& s, int64_t& v) {
    if (s.empty()) return false;
    size_t i = (s[0] == '-') ? 1 : 0;
    if (i == s.size()) return false;
    for (; i < s.size(); ++i)
      if (!std::isdigit(static_cast<unsigned char>(s[i]))) return false;
    try {
      v = std::stoll(s);
    } catch (...) {
      return false;
    }
    return true;
  }

  // "n" or "n/d"
  bool parse_rational(const std::string& s, Rational& r) {
    size_t slash = s.find('/');
    if (slash == std::string::npos) {
      int64_t n;
      if (!parse_int(s, n)) return false;
      r = {n, 1};
      return true;
    }
    int64_t n, d;
    if (!parse_int(s.substr(0, slash), n) || !parse_int(s.substr(slash + 1), d))
      return false;
    if (d == 0) return false;
    r = make_rational(n, d);
    return true;
  }

  bool handle_line(const std::string& raw) {
    std::string line = raw;
    size_t hash = line.find('#');
    if (hash != std::string::npos) line.resize(hash);
    std::istringstream in(line);
    std::string kw;
    if (!(in >> kw)) return true;  // blank / comment-only

    if (kw == "canvas") {
      int64_t w, h;
      std::string sw, sh, extra;
      if (!(in >> sw >> sh) || (in >> extra) || !parse_int(sw, w) ||
          !parse_int(sh, h))
        return fail("canvas expects: canvas <width> <height>");
      if (w <= 0 || h <= 0 || w > kMaxSide || h > kMaxSide)
        return fail("canvas side out of range (1..4096)");
      if (have_canvas) return fail("duplicate canvas");
      m.width = static_cast<uint32_t>(w);
      m.height = static_cast<uint32_t>(h);
      have_canvas = true;
      return true;
    }
    if (kw == "fps") {
      std::string sn, sd, extra;
      if (!(in >> sn)) return fail("fps expects: fps <num> [den]");
      Rational r;
      if (in >> sd) {
        int64_t n, d;
        if (!parse_int(sn, n) || !parse_int(sd, d) || d == 0)
          return fail("fps expects: fps <num> [den]");
        r = make_rational(n, d);
      } else if (!parse_rational(sn, r)) {
        return fail("fps expects: fps <num> [den]");
      }
      if ((in >> extra)) return fail("fps: unexpected extra token");
      if (r.num <= 0) return fail("fps must be positive");
      if (have_fps) return fail("duplicate fps");
      m.fps = r;
      have_fps = true;
      return true;
    }
    if (kw == "clip") {
      std::string gen, sf, extra;
      int64_t f;
      if (!(in >> gen >> sf) || (in >> extra) || !parse_int(sf, f))
        return fail("clip expects: clip <gradient|checker|shapes> <frames>");
      Clip c;
      if (gen == "gradient") c.gen = GenType::Gradient;
      else if (gen == "checker") c.gen = GenType::Checker;
      else if (gen == "shapes") c.gen = GenType::Shapes;
      else return fail("unknown generator: " + gen);
      if (f <= 0) return fail("clip frames must be positive");
      c.frames = f;
      m.clips.push_back(c);
      have_clip = true;
      return true;
    }
    if (kw == "speed") {
      std::string ss, se, sv, extra;
      int64_t s, e;
      Rational v;
      if (!(in >> ss >> se >> sv) || (in >> extra) || !parse_int(ss, s) ||
          !parse_int(se, e) || !parse_rational(sv, v))
        return fail("speed expects: speed <start> <end> <n[/d]>");
      if (!have_clip) return fail("speed must follow a clip line");
      m.clips.back().speeds.push_back({s, e, v});
      return true;
    }
    if (kw == "transition") {
      std::string type, sf, extra;
      int64_t f;
      if (!(in >> type >> sf) || (in >> extra) || !parse_int(sf, f))
        return fail("transition expects: transition <crossfade|wipe|radial> <frames>");
      if (m.clips.empty() || m.transitions.size() != m.clips.size() - 1)
        return fail("transition must follow a clip (no consecutive transitions)");
      Transition t;
      if (type == "crossfade") t.kernel = KernelType::Crossfade;
      else if (type == "wipe") t.kernel = KernelType::Wipe;
      else if (type == "radial") t.kernel = KernelType::Radial;
      else return fail("unknown transition: " + type);
      if (f <= 0) return fail("transition frames must be positive");
      t.frames = f;
      m.transitions.push_back(t);
      have_clip = false;  // speed lines now belong to the next clip
      return true;
    }
    return fail("unknown keyword: " + kw);
  }
};

}  // namespace

bool parse_manifest(const std::string& text, Manifest& out, std::string& err) {
  Manifest m;
  Parser p{text, m, err, 0, false, false, false};
  std::istringstream lines(text);
  std::string line;
  while (std::getline(lines, line)) {
    ++p.line_no;
    if (!p.handle_line(line)) return false;
  }
  if (!p.have_canvas) {
    err = "missing canvas line";
    return false;
  }
  if (m.clips.empty()) {
    err = "manifest has no clips";
    return false;
  }
  out = std::move(m);
  return true;
}

}  // namespace tl
