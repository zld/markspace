// Integration test: reuses the same parser, resolver, tag scanner and libass
// layout probe as the CLI. Exits non-zero on the first failed expectation.
#include <cstdio>
#include <cstdlib>
#include <unistd.h>
#include <filesystem>
#include <string>
#include <vector>

#include "ass_document.hpp"
#include "inline_tags.hpp"
#include "layout_probe.hpp"
#include "report.hpp"
#include "style_resolver.hpp"

namespace {

int g_failures = 0;
int g_checks = 0;

void check(bool condition, const std::string& name) {
    ++g_checks;
    if (!condition) {
        ++g_failures;
        std::fprintf(stderr, "FAIL: %s\n", name.c_str());
    }
}

const sp::RawStyle* find_style(const sp::Document& doc, const std::string& name) {
    for (const sp::RawStyle& s : doc.styles)
        if (s.name == name) return &s;
    return nullptr;
}

bool has_diag(const std::vector<sp::Diagnostic>& diags, const std::string& code) {
    for (const sp::Diagnostic& d : diags)
        if (d.code == code) return true;
    return false;
}

const char* kScript =
    "[Script Info]\n"
    "PlayResX: 640\n"
    "PlayResY: 360\n"
    "\n"
    "[V4+ Styles]\n"
    "Format: Name, Parent, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, "
    "BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, "
    "Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n"
    "Style: Root,,Arial,48,&H00FFFFFF,&H00FFFFFF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,20,20,20,1\n";

// Builds a Style: line for the fixture's custom 24-column format
// (Name, Parent, Fontname, ...). The parent column is always present.
std::string style24(const std::string& name, const std::string& parent = "",
                    const std::string& font = "Arial", const std::string& size = "48",
                    const std::string& bold = "0", const std::string& colour = "&H00FFFFFF",
                    const std::string& outline = "2", const std::string& alignment = "2",
                    const std::string& marginl = "20") {
    return "Style: " + name + "," + parent + "," + font + "," + size + "," + colour +
           ",&H00FFFFFF,&H00000000,&H00000000," + bold + ",0,0,0,100,100,0,0,1," + outline +
           ",0," + alignment + "," + marginl + ",20,20,1\n";
}

}  // namespace

int main(int argc, char** argv) {
    std::string font_dir = argc > 1 ? argv[1] : "/System/Library/Fonts";

    // ---- document parsing -------------------------------------------------
    {
        sp::Document doc = sp::parse_ass_document(kScript, "inline");
        check(doc.info.play_res_x == 640 && doc.info.play_res_y == 360, "script info resolution");
        check(doc.styles.size() == 1, "single style parsed");
        check(doc.styles[0].name == "Root", "style name");
        check(doc.styles[0].fields.at("marginl").value == "20", "style field by name");
        check(find_style(doc, "Root") != nullptr, "style lookup");
    }

    // Explicit empty differs from unspecified.
    {
        std::string s = style24("Child", "Root", "\"\"", "72", "1", "", "3", "8", "");
        sp::Document doc = sp::parse_ass_document(kScript + s, "inline");
        const sp::RawStyle* child = find_style(doc, "Child");
        check(child && child->fields.at("fontname").present, "empty font field present");
        check(child && child->fields.at("fontname").explicit_empty, "empty font field flagged");
        check(child && child->fields.at("fontsize").value == "72", "fontsize parsed");
        check(child && !child->fields.at("marginl").present, "blank margin is unspecified");
        check(child && child->fields.at("alignment").value == "8", "alignment parsed");
    }

    // Field-count and bad timestamps are locatable errors.
    {
        std::string s = kScript;
        s += "Style: Short,Arial,40\n";
        s += "\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n";
        s += "Dialogue: 0,0:00:00.00,0:00:01.00,Root,,0,0,0,,ok\n";
        s += "Dialogue: 0,0:00:00.00,BADTIME,Root,,0,0,0,,bad\n";
        s += "Ticker: 0,0:00:00.00,0:00:01.00,Root,,0,0,0,,x\n";
        sp::Document doc = sp::parse_ass_document(s, "inline");
        check(has_diag(doc.diagnostics, "style-field-count"), "style field count error");
        check(has_diag(doc.diagnostics, "event-time-invalid"), "invalid timestamp error");
        check(has_diag(doc.diagnostics, "event-unknown"), "unknown event type error");
        check(doc.events.size() == 2, "only Dialogue lines become events");
        bool time_line = false;
        for (const sp::Diagnostic& d : doc.diagnostics)
            if (d.code == "event-time-invalid" && d.line == 13) time_line = true;
        check(time_line, "timestamp error carries the line number");
    }

    // ---- style inheritance -------------------------------------------------
    {
        std::string s = kScript;
        s += style24("Mid", "Root", "", "64", "", "&H0000FFFF", "", "");
        s += style24("Leaf", "Mid", "Menlo", "", "1", "", "4", "7");
        sp::Document doc = sp::parse_ass_document(s, "inline");
        auto leaf = sp::resolve_style(doc, "Leaf");
        check(!has_diag(leaf.diagnostics, "style-cycle"), "clean chain has no cycle");
        check(leaf.chain.size() == 3, "chain has three links");
        check(leaf.chain.front().name == "Root" && leaf.chain.back().name == "Leaf", "chain is root->leaf");
        check(leaf.style.font_name.value == "Menlo", "leaf font wins");
        check(leaf.style.font_size.value == 64, "font size inherited from mid");
        check(leaf.style.primary_colour.value == 0x0000FFFF, "colour inherited from mid");
        check(leaf.style.bold.value == 1, "bold from leaf");
        check(leaf.style.outline.value == 4, "outline fallback default");
        check(leaf.style.margin_l.value == 20, "margin inherited from root");
    }

    // Font cleared by an explicit empty at the leaf.
    {
        std::string s = kScript;
        s += style24("NoFont", "Root", "\"\"", "\"\"", "\"\"", "\"\"", "", "", "");
        sp::Document doc = sp::parse_ass_document(s, "inline");
        auto res = sp::resolve_style(doc, "NoFont");
        check(res.style.font_cleared(), "explicit empty font clears the face");
    }

    // Missing parent terminates with a diagnostic.
    {
        std::string s = kScript;
        s += style24("Lost", "Ghost", "Arial", "40", "", "", "", "");
        sp::Document doc = sp::parse_ass_document(s, "inline");
        auto res = sp::resolve_style(doc, "Lost");
        check(has_diag(res.diagnostics, "parent-missing"), "missing parent reported");
        check(res.chain.back().name == "Lost", "chain still ends at the requested style");
    }

    // Cycles are reported and do not recurse forever.
    {
        std::string s = kScript;
        s += style24("A", "B", "Arial", "40", "", "", "", "");
        s += style24("B", "A", "Arial", "40", "", "", "", "");
        sp::Document doc = sp::parse_ass_document(s, "inline");
        auto res = sp::resolve_style(doc, "A");
        check(has_diag(res.diagnostics, "style-cycle"), "cycle reported");
        check(res.chain.size() <= 3, "cycle walk terminates");
    }

    // Duplicate names are reported and first definition wins.
    {
        std::string s = kScript;
        s += style24("Dup", "", "Arial", "40", "", "", "", "");
        s += style24("Dup", "", "Menlo", "80", "", "", "", "");
        sp::Document doc = sp::parse_ass_document(s, "inline");
        check(has_diag(doc.diagnostics, "style-duplicate-name"), "duplicate name reported");
        check(doc.styles.size() == 2, "duplicate is not appended");
        check(find_style(doc, "Dup")->fields.at("fontname").value == "Arial", "first definition wins");
    }

    // Numeric clamping records the original and the fallback.
    {
        std::string s = kScript;
        s += style24("Clamp", "", "Arial", "99999", "7", "", "500", "42", "");
        sp::Document doc = sp::parse_ass_document(s, "inline");
        auto res = sp::resolve_style(doc, "Clamp");
        check(res.style.font_size.value == 1024, "font size clamped");
        check(res.style.bold.value == 1, "bold clamped to 1");
        check(res.style.alignment.value == 9, "alignment clamped to 9");
        check(res.style.adjustments.size() >= 3, "clamps recorded");
        check(res.style.adjustments[0].original == "99999", "original value preserved");
    }

    // ---- inline tags -------------------------------------------------------
    {
        sp::InlineParse p = sp::parse_inline_tags(
            R"({\b1}bold{\b0} and {\i1\c&H00FF00&}green{\i0})", 0, 1);
        check(p.counts.bold == 2 && p.counts.italic == 2, "bold/italic counted");
        check(p.counts.colour == 1, "colour counted");
        check(p.normalized_text == "bold and green", "normalised text");
        check(p.diagnostics.empty(), "no diagnostics for well-formed tags");
        check(p.overrides.trace.size() == 5, "trace records every applied tag");
    }
    {
        sp::InlineParse p = sp::parse_inline_tags(R"({\fnMenlo}mono{\rRoot}x)", 0, 1);
        check(p.counts.font_name == 1, "font name tag recognised");
        check(p.counts.reset_style == 1, "reset with style target counted");
        check(p.overrides.last_reset_target == "Root", "reset target captured");
    }
    {
        sp::InlineParse p = sp::parse_inline_tags(R"({\b1\foobar9 unclosed)", 0, 1);
        check(p.counts.unknown >= 1, "unknown tag counted");
        check(has_diag(p.diagnostics, "tag-unknown"), "unknown tag reported");
        check(has_diag(p.diagnostics, "tag-unclosed"), "unclosed block reported");
    }
    {
        sp::InlineParse p = sp::parse_inline_tags(R"(a\Nb\nc\hd)", 0, 1);
        check(p.counts.line_break == 2, "\\N and \\n counted as line breaks");
        check(p.normalized_text == "a b c d", "escapes become spaces");
    }
    {
        sp::InlineParse p = sp::parse_inline_tags(R"({\clip(0,0,10,10)}clipped{\t(\fs20)}x)", 0, 1);
        check(p.counts.clip == 1, "clip tag counted");
        check(p.diagnostics.empty(), "nested parentheses parsed without error");
    }
    {
        check(sp::canonicalize_for_render(R"({\b1 x)") == R"({\b1 x})", "unclosed block closed canonically");
        check(sp::canonicalize_for_render(R"({\b1}x)") == R"({\b1}x)", "balanced block untouched");
    }
    std::printf("integration checks: %d, failures: %d\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}
