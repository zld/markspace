#include "test_cases.hpp"

#include <cstdio>
#include <string>

#include "fixture_builder.hpp"
#include "style_parser.hpp"
#include "tag_resolver.hpp"
#include "test_harness.hpp"

namespace asspipe::test {

namespace {

AssDocument parse(const std::string& s) {
    auto doc = asspipe::parse_ass(s, "test.ass");
    asspipe::build_inheritance_graph(doc);
    return doc;
}

int count_kind(const AssDocument& doc, asspipe::DiagKind kind) {
    int n = 0;
    for (const auto& d : doc.diagnostics)
        if (d.kind == kind) ++n;
    return n;
}

void test_sections_and_fields() {
    std::puts("[parser] sections, comma/empty fields, UTF-8");
    const char* src =
        "[Script Info]\nTitle: t\n\n[V4+ Styles]\n"
        "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
        "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, "
        "ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, "
        "Alignment, MarginL, MarginR, MarginV, Encoding\n"
        "Style: Root,Arial,40,&H00FFFFFF,&H000000FF,&H00000000,"
        "&H64000000,0,0,0,0,100,100,0,0,1,2,1,2,40,40,40,1\n"
        "Style: Empty,,0,,,,, , ,0,0,100,100,0,0,1,,,2,,,,1\n"
        "[Events]\n"
        "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, "
        "Effect, Text\n"
        "Dialogue: 0,0:00:01.50,0:00:02.00,Root,,0,0,0,,hello 世界 café\n";
    auto doc = parse(src);
    CHECK(doc.styles.size() == 2, static_cast<int>(doc.styles.size()));
    CHECK(doc.events.size() == 1, static_cast<int>(doc.events.size()));
    auto root = doc.styles.find("root")->second;
    CHECK(root.fontsize == 40, root.fontsize);
    CHECK(root.line >= 5, root.line);
    const auto& ev = doc.events.front();
    CHECK(ev.start_cs == 150, ev.start_cs);
    CHECK(ev.end_cs == 200, ev.end_cs);
    CHECK(ev.text.find("café") != std::string::npos, 0);
    CHECK(ev.text.find("世界") != std::string::npos, 0);
}

void test_timestamps() {
    std::puts("[parser] timestamp validation with line numbers");
    long cs = 0;
    CHECK(asspipe::parse_timestamp("1:02:03.45", cs) && cs == 372345, cs);
    CHECK(!asspipe::parse_timestamp("0:60:00.00", cs), 0);
    CHECK(!asspipe::parse_timestamp("0:00:00", cs), 0);
    CHECK(!asspipe::parse_timestamp("garbage", cs), 0);
    const char* src =
        "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, "
        "MarginR, MarginV, Effect, Text\n"
        "Dialogue: 0,bad,0:00:01.00,S,,0,0,0,,x\n";
    auto doc = parse(src);
    CHECK(count_kind(doc, asspipe::DiagKind::BadTimestamp) == 1,
          count_kind(doc, asspipe::DiagKind::BadTimestamp));
    CHECK(doc.diagnostics.front().line == 3, doc.diagnostics.front().line);
    CHECK(doc.diagnostics.front().event_index == 1,
          doc.diagnostics.front().event_index);
}

void test_inheritance_chain() {
    std::puts("[parser] ancestor chain and field resolution");
    const char* src =
        "[V4+ Styles]\n"
        "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
        "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, "
        "ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, "
        "Alignment, MarginL, MarginR, MarginV, Encoding\n"
        "Style: Grand,Arial,50,&H00112233,&H000000FF,&H00000000,"
        "&H64000000,1,1,0,0,100,100,0,0,1,2,1,2,40,40,40,1\n"
        "Style: Parent,Helvetica,0,,&H000000FF,&H00000000,"
        "&H64000000,-1,-1,0,0,100,100,0,0,1,2,1,2,,,40,1\n"
        "Style: Child,,44,,&H000000FF,&H00000000,"
        "&H64000000,0,-1,0,0,100,100,0,0,1,2,1,2,40,40,40,1\n"
        "[Inheritance]\nInherit: Parent = Grand\nInherit: Child = Parent\n";
    auto doc = parse(src);
    const auto& child = doc.styles.find("child")->second;
    CHECK(child.ancestors.size() == 2,
          static_cast<int>(child.ancestors.size()));
    CHECK(child.ancestors[0] == "parent", 0);
    CHECK(child.ancestors[1] == "grand", 0);
    auto rs = asspipe::resolve_style(doc, child);
    CHECK_STR(rs.fontname == "Helvetica", rs.fontname); // nearest explicit
    CHECK(rs.fontsize == 44, rs.fontsize);               // child explicit
    CHECK(rs.bold == 0, rs.bold);                        // child overrides
    CHECK(rs.italic == 1, rs.italic);                    // walks to Grand
    CHECK_STR(rs.primary_colour == "&H00112233", rs.primary_colour);
}

void test_inheritance_defects() {
    std::puts("[parser] missing parent, self inheritance, cycle");
    auto doc = parse(build_demo_ass());
    CHECK(doc.inheritance_errors == 2, doc.inheritance_errors);
    CHECK(count_kind(doc, asspipe::DiagKind::MissingParent) == 1, 0);
    CHECK(count_kind(doc, asspipe::DiagKind::InheritanceCycle) == 2, 0);
    CHECK(!doc.styles.find("ghost")->second.valid, 0);
    CHECK(!doc.styles.find("loopa")->second.valid, 0);
    CHECK(!doc.styles.find("loopb")->second.valid, 0);
    CHECK(doc.styles.find("base")->second.valid, 0);
    // Self inheritance.
    const char* self_src =
        "[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, "
        "SecondaryColour, OutlineColour, BackColour, Bold, Italic, "
        "Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, "
        "Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n"
        "Style: Solo,Arial,40,&H00FFFFFF,&H000000FF,&H00000000,"
        "&H64000000,0,0,0,0,100,100,0,0,1,2,1,2,40,40,40,1\n"
        "[Inheritance]\nInherit: Solo = Solo\n";
    auto self_doc = parse(self_src);
    CHECK(count_kind(self_doc, asspipe::DiagKind::SelfInheritance) == 1, 0);
    CHECK(!self_doc.styles.find("solo")->second.valid, 0);
}

void test_tags_order_and_reset() {
    std::puts("[tags] ordered application, reset, per-type counts");
    asspipe::TagCounts counts;
    bool unclosed = false;
    std::string visible;
    auto state = asspipe::resolve_tags(
        R"(keep {\\b1\i1\fs30} mid {\r} end)", counts, unclosed, visible);
    // Text outside override blocks is preserved verbatim.
    CHECK_STR(visible == "keep  mid  end", visible);
    CHECK(!unclosed, 0);
    CHECK(counts.b == 1 && counts.i == 1 && counts.fs == 1 && counts.r == 1,
          counts.total());
    // The final \r resets every override back to the style defaults.
    CHECK(state.bold == -1 && state.italic == -1 && state.fontsize == 0, 0);

    asspipe::TagCounts c2;
    std::string vis2;
    auto s2 = asspipe::resolve_tags(R"(x {\rNamed} y)", c2, unclosed, vis2);
    CHECK_STR(s2.style_name == "Named", s2.style_name);
    CHECK(c2.r == 1, c2.r);
}

void test_tag_edge_cases() {
    std::puts("[tags] empty/adjacent blocks, unclosed, unknown, no loss");
    asspipe::TagCounts counts;
    bool unclosed = false;
    std::string visible;
    // Closed blocks contribute b and the unknown \fad; the unclosed block
    // contains a clean \i1 token then prose that must stay literal.
    asspipe::resolve_tags(
        R"({}{\b1}{\fad(1,2)} tail {\i1\no-close visible)",
        counts, unclosed, visible);
    CHECK(unclosed, 0);
    CHECK(counts.unknown == 1, counts.unknown); // \fad inside closed block
    // b and i each appear once; the empty block contributes an r count.
    CHECK(counts.b == 1 && counts.i == 1 && counts.r == 1, counts.total());
    // Prose inside the unclosed block survives as literal text...
    CHECK(visible.find("tail") != std::string::npos, 0);
    CHECK(visible.find("visible") != std::string::npos, 0);
    CHECK(visible.find("no-close") != std::string::npos, 0);
    // ...and a half-written tag fragment (space before the name) is never
    // mistaken for a real tag or silently dropped.
    asspipe::TagCounts c3;
    bool uc3 = false;
    std::string vis3;
    asspipe::resolve_tags(R"({garbage \b tail)", c3, uc3, vis3);
    CHECK(c3.b == 0, c3.b);
    CHECK(uc3, 0);
    CHECK(vis3.find("garbage") != std::string::npos, 0);
    CHECK(vis3.find("tail") != std::string::npos, 0);
    CHECK(vis3.find("\\b") != std::string::npos, 0);

    // Fixture-wide aggregate matches the acceptance contract.
    auto doc = parse(build_demo_ass());
    int total = 0, unknown = 0, unclosed_events = 0;
    for (auto& ev : doc.events) {
        asspipe::resolve_event_tags(ev);
        total += ev.tags.total();
        unknown += ev.tags.unknown;
        if (ev.unclosed_block) ++unclosed_events;
    }
    CHECK(total == 48, total);
    CHECK(unknown == 1, unknown);
    CHECK(unclosed_events == 1, unclosed_events);
}

} // namespace

void run_parser_tests() {
    test_sections_and_fields();
    test_timestamps();
    test_inheritance_chain();
    test_inheritance_defects();
    test_tags_order_and_reset();
    test_tag_edge_cases();
}

} // namespace asspipe::test
