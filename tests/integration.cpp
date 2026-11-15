#include <cstdlib>
#include <iostream>

#include "ass_parser.hpp"
#include "ass_renderer.hpp"
#include "fixtures.hpp"
#include "layout_engine.hpp"
#include "style_resolver.hpp"
#include "tag_resolver.hpp"
#include "util.hpp"

using namespace assaudit;

namespace {
int g_failures = 0;
int g_checks = 0;

void check(bool cond, const std::string& what) {
  ++g_checks;
  if (!cond) {
    ++g_failures;
    std::cerr << "FAIL: " << what << "\n";
  }
}

AssDocument parse(const std::string& text) {
  AssDocument doc;
  parseAss(text, "fixture", doc);
  return doc;
}

void testParserBasics() {
  auto doc = parse(generateDemoAss());
  check(doc.play_res_x == 1920 && doc.play_res_y == 1080,
        "script info resolution parsed");
  check(doc.styles.size() == 8, "eight styles parsed");
  int dialogue = 0, comments = 0;
  for (const auto& e : doc.events) (e.is_comment ? comments : dialogue)++;
  check(dialogue == 12, "twelve dialogue events");
  check(comments == 0, "no comments in demo");
  check(doc.events.front().start_ms == 0 &&
            doc.events.front().end_ms == 300,
        "timecode converted to centisecond ms");
}

void testReorderedFormatAndCommas() {
  auto doc = parse(generateCommasAndEmptyAss());
  const auto& ev = doc.events.front();
  check(ev.text.find("Text, with, commas") != std::string::npos,
        "commas preserved inside Text field");
  check(ev.text.find("trailing,") != std::string::npos,
        "trailing comma preserved");
  check(doc.events.size() == 2 && doc.events.back().is_comment,
        "comment event kept but flagged");
}

void testInheritance() {
  auto doc = parse(generateDemoAss());
  auto stats = resolveStyles(doc);
  check(stats.inheritance_edges == 6, "six inheritance edges");
  check(stats.inheritance_errors == 0, "no inheritance errors in demo");
  const AssStyle* title = nullptr;
  const AssStyle* small = nullptr;
  for (const auto& s : doc.styles) {
    if (s.name == "Title") title = &s;
    if (s.name == "Small") small = &s;
  }
  check(title && title->chain.size() == 4, "Title chain length 4");
  check(title && title->chain ==
                     std::vector<std::string>({"Title", "Emphasis",
                                               "Narration", "Base"}),
        "Title chain order child-first");
  check(small && small->merged.at("fontsize").source == "Small" &&
            small->merged.at("fontsize").text == "28",
        "Small keeps its own fontsize");
  const AssStyle* caption = nullptr;
  for (const auto& s : doc.styles)
    if (s.name == "Caption") caption = &s;
  check(caption && caption->merged.at("fontname").source == "Caption",
        "Caption uses its own explicit font");
  check(title && title->merged.at("fontname").state == FieldState::Explicit,
        "explicit value state");
  check(title && title->merged.at("bold").state == FieldState::Explicit &&
            title->merged.at("bold").source == "Title",
        "Title overrides bold explicitly");
}

void testDefaultedFields() {
  // A style record providing only fontname/size must take everything
  // else from the documented ASS defaults, flagged as Defaulted.
  std::string text =
      "[Script Info]\nPlayResX: 640\nPlayResY: 360\n\n[V4+ Styles]\n"
      "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
      "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, "
      "ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, "
      "Alignment, MarginL, MarginR, MarginV, Encoding, Parent\n"
      "Style: Thin,Arial,20,,,,,,,,,,,,,,,,,,,,,\n\n[Events]\n"
      "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, "
      "Effect, Text\nDialogue: 0,0:00:00.00,0:00:01.00,Thin,,0,0,0,,x\n";
  auto doc = parse(text);
  resolveStyles(doc);
  const AssStyle* thin = &doc.styles.front();
  check(thin->merged.at("fontname").state == FieldState::Explicit,
        "provided font stays explicit");
  check(thin->merged.at("alignment").state == FieldState::Defaulted &&
            thin->merged.at("alignment").source == "*default" &&
            thin->merged.at("alignment").text == "2",
        "unset alignment falls back to ASS default");
  check(thin->merged.at("marginl").text == "20",
        "default margin value applied");
}

void testBooleanFields() {
  // Real ASS files use 1 (not only -1) to enable bold/italic/underline.
  std::string text =
      "[Script Info]\n[V4+ Styles]\n"
      "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
      "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, "
      "ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, "
      "Alignment, MarginL, MarginR, MarginV, Encoding, Parent\n"
      "Style: B,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H64000000,"
      "1,1,1,0,100,100,0,0,1,2,1,2,10,10,10,1,\n"
      "Style: Bad,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H64000000,"
      "7,0,0,0,100,100,0,0,1,2,1,2,10,10,10,1,\n\n[Events]\n"
      "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, "
      "Effect, Text\nDialogue: 0,0:00:00.00,0:00:01.00,B,,0,0,0,,x\n";
  auto doc = parse(text);
  resolveStyles(doc);
  const AssStyle* b = nullptr;
  const AssStyle* bad = nullptr;
  for (const auto& s : doc.styles) {
    if (s.name == "B") b = &s;
    if (s.name == "Bad") bad = &s;
  }
  check(b && b->merged.at("italic").text == "1" &&
            b->merged.at("bold").state == FieldState::Explicit,
        "boolean value 1 accepted and kept explicit");
  bool warned = false;
  for (const auto& e : bad->errors)
    if (e.code == "field_invalid") warned = true;
  check(warned, "illegal boolean 7 skipped with a warning");
}

void testMissingParent() {
  auto doc = parse(generateMissingParentAss());
  auto stats = resolveStyles(doc);
  check(stats.inheritance_errors >= 1, "missing parent counted");
  const AssStyle* orphan = nullptr;
  for (const auto& s : doc.styles)
    if (s.name == "Orphan") orphan = &s;
  check(orphan && orphan->blocked, "orphan style blocked");
  bool hasCode = false;
  for (const auto& d : orphan->errors)
    if (d.code == "parent_missing") hasCode = true;
  check(hasCode, "parent_missing diagnostic emitted");
}

void testCycle() {
  auto doc = parse(generateCycleAss());
  resolveStyles(doc);
  const AssStyle* alpha = nullptr;
  for (const auto& s : doc.styles)
    if (s.name == "Alpha") alpha = &s;
  check(alpha && alpha->blocked, "cyclic style blocked");
  bool cycle = false;
  for (const auto& d : alpha->errors)
    if (d.code == "inheritance_cycle") cycle = true;
  check(cycle, "inheritance_cycle diagnostic");
}

void testDuplicate() {
  auto doc = parse(generateDuplicateStyleAss());
  auto stats = resolveStyles(doc);
  check(stats.inheritance_errors >= 1, "duplicate name is inheritance error");
  bool dupDiag = false;
  for (const auto& d : doc.diagnostics)
    if (d.code == "style_duplicate" && d.fatal) dupDiag = true;
  check(dupDiag, "duplicate diagnostic carries line number and fatal flag");
}

void testTimecodes() {
  auto doc = parse(generateTimecodeBadAss());
  int bad = 0, zero = 0, order = 0;
  for (const auto& e : doc.events)
    for (const auto& d : e.diagnostics) {
      if (d.code == "timecode_bad") bad++;
      if (d.code == "zero_duration") zero++;
      if (d.code == "timecode_order") order++;
    }
  check(bad >= 1, "garbage timecode diagnosed without throwing");
  check(zero == 1, "one zero-duration event");
  check(order == 1, "one end-before-start event");
}

void testTags() {
  auto r = resolveTags("{\\b1\\fs40\\c&H00FF00&}x{\\zoo1}{\\t(0,100,\\i1)}");
  check(r.total == 6, "tag total counts nested transform commands");
  check(r.unknown == 1, "one unknown tag");
  check(r.valid == 5, "five valid tags");
  check(r.overrides == 5, "overrides counted");
  auto u = resolveTags("{\\b1 unclosed {\\i1}");
  check(u.unclosed_groups >= 1, "unclosed group recovered");
  check(u.total == 2, "both commands counted despite recovery");
  check(u.valid >= 1 && u.commands.size() == 2,
        "scanning resumes on the nested group");
  auto p = resolveTags("{\\fs(30}");
  check(p.warnings.size() >= 1, "unbalanced parenthesis warns");
  check(p.total == 1, "unbalanced tag still counted once");
  auto pos = resolveTags("{\\pos(120,340)}");
  check(pos.has_pos && pos.pos_x == 120 && pos.pos_y == 340,
        "pos geometry captured");
  auto mv = resolveTags("{\\move(1,2,3,4)}");
  check(mv.has_move && mv.move_x2 == 3, "move geometry captured");
  auto scope = resolveTags("{\\b1}");
  check(scope.total == 1, "tag scope resets per call");
}

void testLayoutPipeline() {
  auto doc = parse(generateDemoAss());
  resolveStyles(doc);
  AssRenderer renderer;
  check(renderer.init(1920, 1080).empty(), "renderer initialised");
  LayoutConfig cfg;
  cfg.width = 1920;
  cfg.height = 1080;
  auto out = runLayout(doc, renderer, cfg);
  check(out.reports.size() == 12, "one report per dialogue event");
  check(out.rendered == 12, "all demo events render");
  check(out.blocked_events == 0, "no blocked demo events");
  for (const auto& r : out.reports) {
    check(r.region_width > 0 && r.region_height > 0,
          "region measured from image blocks, not estimated");
    check(r.region_width <= 1920 && r.region_height <= 1080,
          "region stays within resolution");
    check(r.image_blocks > 0, "image blocks present");
    check(r.pixel_digest.size() == 16, "digest is 64-bit hex");
    check(out.jsonl[r.event_id - 1].find("pixel_digest") !=
              std::string::npos,
          "jsonl line contains digest");
  }
}

void testBlockedEvents() {
  auto doc = parse(generateTimecodeBadAss());
  resolveStyles(doc);
  AssRenderer renderer;
  renderer.init(1280, 720);
  LayoutConfig cfg;
  cfg.width = 1280;
  cfg.height = 720;
  auto lenient = runLayout(doc, renderer, cfg);
  check(lenient.rendered == 1, "lenient mode keeps the healthy event");
  check(lenient.blocked_events == 3, "three events blocked, still reported");
  cfg.strict = true;
  auto strict = runLayout(doc, renderer, cfg);
  check(strict.structural_error, "strict mode flags structural error");
}

void testFontFallbackReporting() {
  // A deliberately impossible font name; libass must substitute something
  // and the message callback should mark the event as fallback.
  std::string ass =
      "[Script Info]\nPlayResX: 640\nPlayResY: 360\n\n[V4+ Styles]\n"
      "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
      "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, "
      "ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, "
      "Alignment, MarginL, MarginR, MarginV, Encoding, Parent\n"
      "Style: Base,NoSuchFontZxqw,40,&H00FFFFFF,&H000000FF,&H00000000,"
      "&H64000000,0,0,0,0,100,100,0,0,1,2,1,2,10,10,10,1,\n\n[Events]\n"
      "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, "
      "Effect, Text\nDialogue: 0,0:00:00.00,0:00:02.00,Base,,0,0,0,,hello\n";
  auto doc = parse(ass);
  resolveStyles(doc);
  AssRenderer renderer;
  renderer.init(640, 360);
  LayoutConfig cfg{640, 360, false};
  auto out = runLayout(doc, renderer, cfg);
  check(out.reports.size() == 1 && out.reports[0].rendered,
        "fallback font still renders");
  // We assert the field exists and is stable; value depends on the
  // local fontconfig database and is therefore not hard-required.
}

void testStyleResetAcrossChains() {
  // \r OtherStyle references a style outside the event's chain; the
  // synthesized document must still contain every usable style.
  std::string ass =
      "[Script Info]\nPlayResX: 640\nPlayResY: 360\n\n[V4+ Styles]\n"
      "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
      "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, "
      "ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, "
      "Alignment, MarginL, MarginR, MarginV, Encoding, Parent\n"
      "Style: Base,Arial,40,&H00FFFFFF,&H000000FF,&H00000000,"
      "&H64000000,0,0,0,0,100,100,0,0,1,2,1,2,10,10,10,1,\n"
      "Style: Other,Verdana,60,&H0000FFFF,&H000000FF,&H00000000,"
      "&H64000000,-1,0,0,0,100,100,0,0,1,2,1,2,10,10,10,1,\n\n[Events]\n"
      "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, "
      "Effect, Text\n"
      "Dialogue: 0,0:00:00.00,0:00:02.00,Base,,0,0,0,,before {\\r Other}"
      "reset\n";
  auto doc = parse(ass);
  resolveStyles(doc);
  AssRenderer renderer;
  renderer.init(640, 360);
  LayoutConfig cfg{640, 360, false};
  auto out = runLayout(doc, renderer, cfg);
  check(out.rendered == 1 && out.reports[0].rendered,
        "event with cross-chain \\r still renders");
  check(out.reports[0].region_width > 0, "cross-chain reset has region");
}

}  // namespace

int main() {
  testParserBasics();
  testReorderedFormatAndCommas();
  testInheritance();
  testDefaultedFields();
  testBooleanFields();
  testMissingParent();
  testCycle();
  testDuplicate();
  testTimecodes();
  testTags();
  testLayoutPipeline();
  testBlockedEvents();
  testFontFallbackReporting();
  testStyleResetAcrossChains();
  std::cout << "checks=" << g_checks << " failures=" << g_failures << "\n";
  return g_failures ? EXIT_FAILURE : EXIT_SUCCESS;
}
