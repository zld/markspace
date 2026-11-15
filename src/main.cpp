#include <algorithm>
#include <fstream>
#include <iostream>
#include <sstream>

#include "ass_parser.hpp"
#include "ass_renderer.hpp"
#include "fixtures.hpp"
#include "layout_engine.hpp"
#include "style_resolver.hpp"
#include "tag_resolver.hpp"
#include "util.hpp"

namespace {

enum ExitCode {
  OK = 0,
  ARG_ERROR = 2,
  STRICT_STRUCTURAL = 3,
  RENDER_FAILURE = 4,
};

struct Options {
  bool check = false;
  bool render = false;
  bool strict = false;
  bool demo = false;
  bool verbose = false;
  int width = 1920;
  int height = 1080;
  std::string input;
  std::string output;
};

void usage() {
  std::cerr
      << "usage: subtitle_audit (--check|--render) [--strict] [--demo]\n"
      << "                       [--resolution WxH] [--output FILE] [INPUT]\n"
      << "  --check        parse, resolve and diagnose without rendering\n"
      << "  --render       full pipeline incl. offscreen libass rendering\n"
      << "  --strict       structural errors cause exit code 3\n"
      << "  --demo         use the built-in generated demo document\n"
      << "  --verbose      print every merged field and its provenance\n"
      << "  --resolution   target video size, e.g. 1920x1080 (default)\n"
      << "  --output FILE  write per-event JSONL image summaries\n";
}

bool parseResolution(const std::string& s, int& w, int& h) {
  auto x = s.find('x');
  if (x == std::string::npos) x = s.find('X');
  if (x == std::string::npos) return false;
  return assaudit::parseInt(s.substr(0, x), w) &&
         assaudit::parseInt(s.substr(x + 1), h) && w > 0 && h > 0 &&
         w <= 16384 && h <= 16384;
}

}  // namespace

int main(int argc, char** argv) {
  Options opt;
  for (int i = 1; i < argc; ++i) {
    std::string a = argv[i];
    auto next = [&](std::string& dst) {
      if (i + 1 >= argc) return false;
      dst = argv[++i];
      return true;
    };
    if (a == "--check") opt.check = true;
    else if (a == "--render") opt.render = true;
    else if (a == "--strict") opt.strict = true;
    else if (a == "--demo") opt.demo = true;
    else if (a == "--verbose" || a == "-v") opt.verbose = true;
    else if (a == "--resolution") {
      std::string v;
      if (!next(v) || !parseResolution(v, opt.width, opt.height)) {
        std::cerr << "error: bad --resolution value\n";
        return ARG_ERROR;
      }
    } else if (a == "--output") {
      if (!next(opt.output)) {
        std::cerr << "error: --output requires a path\n";
        return ARG_ERROR;
      }
    } else if (!a.empty() && a[0] == '-') {
      std::cerr << "error: unknown option " << a << "\n";
      usage();
      return ARG_ERROR;
    } else {
      opt.input = a;
    }
  }
  if (!opt.check && !opt.render) {
    std::cerr << "error: one of --check or --render is required\n";
    usage();
    return ARG_ERROR;
  }

  std::string content;
  std::string label;
  if (opt.demo || opt.input.empty()) {
    opt.demo = true;
    content = assaudit::generateDemoAss();
    label = "<generated-demo>";
  } else {
    std::ifstream f(opt.input, std::ios::binary);
    if (!f) {
      std::cerr << "error: cannot read input '" << opt.input << "'\n";
      return ARG_ERROR;
    }
    std::ostringstream ss;
    ss << f.rdbuf();
    content = ss.str();
    label = opt.input;
  }

  assaudit::AssDocument doc;
  assaudit::parseAss(content, label, doc);
  auto styleStats = assaudit::resolveStyles(doc);

  int tagTotal = 0, tagUnknown = 0;
  for (const auto& ev : doc.events) {
    if (ev.is_comment) continue;
    auto r = assaudit::resolveTags(ev.text);
    tagTotal += r.total;
    tagUnknown += r.unknown;
  }

  std::cout << "styles=" << doc.styles.size()
            << " inheritance_edges=" << styleStats.inheritance_edges
            << " inheritance_errors=" << styleStats.inheritance_errors
            << "\n";
  for (const auto& st : doc.styles) {
    std::cout << "style " << st.name << " line=" << st.line
              << (st.blocked ? " BLOCKED" : "") << " chain=";
    for (size_t i = 0; i < st.chain.size(); ++i)
      std::cout << (i ? "->" : "") << st.chain[i];
    if (st.chain.empty()) std::cout << "(none)";
    std::cout << "\n";
    if (opt.verbose && !st.blocked) {
      for (const auto& [key, field] : st.merged)
        std::cout << "  " << key << "=" << field.text << " <"
                  << (field.state == assaudit::FieldState::Defaulted
                          ? "default"
                          : field.source)
                  << ": " << field.source_line << ">\n";
    }
    for (const auto& d : st.errors)
      std::cerr << label << ":" << d.line << ": " << d.code << ": "
                << d.message << "\n";
  }
  int eventCount = static_cast<int>(std::count_if(
      doc.events.begin(), doc.events.end(),
      [](const assaudit::AssEvent& e) { return !e.is_comment; }));

  assaudit::LayoutOutcome outcome;
  if (opt.render) {
    assaudit::AssRenderer renderer;
    std::string err = renderer.init(opt.width, opt.height);
    if (!err.empty()) {
      std::cerr << "error: renderer init: " << err << "\n";
      return RENDER_FAILURE;
    }
    assaudit::LayoutConfig cfg;
    cfg.width = opt.width;
    cfg.height = opt.height;
    cfg.strict = opt.strict;
    outcome = assaudit::runLayout(doc, renderer, cfg);

    if (!opt.output.empty()) {
      std::ofstream jf(opt.output);
      if (!jf) {
        std::cerr << "error: cannot open output '" << opt.output
                  << "' for writing (event-level results discarded)\n";
        return RENDER_FAILURE;
      }
      for (const auto& line : outcome.jsonl) jf << line << "\n";
      if (!jf) {
        std::cerr << "error: failed while writing '" << opt.output << "'\n";
        return RENDER_FAILURE;
      }
    }
    std::cout << "events=" << eventCount << " tags_total=" << tagTotal
              << " tags_unknown=" << tagUnknown
              << " rendered=" << outcome.rendered << "\n";
    for (const auto& r : outcome.reports) {
      std::cout << "event_id=" << r.event_id << " tag_valid=" << r.tag_valid
                << " tag_override=" << r.tag_override
                << " region_width=" << r.region_width
                << " region_height=" << r.region_height
                << " image_blocks=" << r.image_blocks
                << " pixel_digest=" << r.pixel_digest << "\n";
    }
  }

  for (const auto& d : doc.diagnostics)
    std::cerr << label << ":" << d.line << ": " << d.code << ": " << d.message
              << "\n";
  if (!opt.render)
    std::cout << "events=" << eventCount << " tags_total=" << tagTotal
              << " tags_unknown=" << tagUnknown << " rendered=0 (check mode)\n";
  if (outcome.render_failure) return RENDER_FAILURE;
  if (opt.strict) {
    for (const auto& d : doc.diagnostics)
      if (d.fatal) return STRICT_STRUCTURAL;
    if (outcome.structural_error) return STRICT_STRUCTURAL;
  }
  return OK;
}
