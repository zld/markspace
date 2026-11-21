#include "fixtures.h"

#include <cstdlib>
#include <sstream>
#include <string>

namespace {

void append_escaped(std::ostringstream& out, const std::string& utf8) {
    out << '"';
    for (const unsigned char c : utf8) {
        if (c == '"' || c == '\\') {
            out << '\\' << static_cast<char>(c);
        } else if (c == '\n') {
            out << "\\n";
        } else if (c == '\t') {
            out << "\\t";
        } else if (c < 0x20) {
            out << "\\u00";
            const char hex[] = "0123456789abcdef";
            out << hex[(c >> 4) & 0xF] << hex[c & 0xF];
        } else {
            // Raw UTF-8 bytes are legal inside JSON strings.
            out << static_cast<char>(c);
        }
    }
    out << '"';
}

void append_item(std::ostringstream& out, bool& first, const char* id,
                 const char* language, const std::string& text) {
    if (!first) {
        out << ",\n";
    }
    first = false;
    out << "    {\"id\": ";
    append_escaped(out, id);
    out << ", \"language\": ";
    append_escaped(out, language);
    out << ", \"text\": ";
    append_escaped(out, text);
    out << "}";
}

}  // namespace

std::string make_fixture_json() {
    // U+0301 combining acute, U+FE0F variation selector, U+200D zero width
    // joiner and U+4E2D (the sole missing glyph in Arial) are written as raw
    // UTF-8 so the fixture also exercises the decoder.
    const std::string combining = "Cafe\xCC\x81 " "de\xCC\x81ja\xCC\x80 vu";
    const std::string emoji =
        "Smile \xE2\x98\xBA\xEF\xB8\x8F a\xE2\x80\x8D" "b now";
    const std::string long_word =
        "MMMMMMMMMMMMMMMMMMMMMMMMMMMMMM";  // 30 * 50 px at size 40
    const std::string wrapped =
        "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu "
        "xi omicron";
    const std::string missing = "{t=0}Portal \xE4\xB8\xAD" " opens{t=750}";
    const std::string literal = "{{not-a-marker}} stays visible";

    std::ostringstream out;
    out << "{\n";
    const char* path = std::getenv("SUBTITLE_METRICS_FONT");
    if (path == nullptr || *path == '\0') {
        path = "/System/Library/Fonts/Supplemental/Arial.ttf";
    }
    out << "  \"font\": {\"path\": ";
    append_escaped(out, path);
    out << ",\n";
    out << "            \"size\": 40, \"letter_spacing\": 0, \"line_spacing\": 1.25,\n";
    out << "            \"missing_glyph\": \"fallback\"},\n";
    out << "  \"canvas\": {\"width\": 640, \"safe_inset\": 32},\n";
    out << "  \"max_lines\": 2,\n";
    out << "  \"subtitles\": [\n";
    bool first = true;
    append_item(out, first, "line-01-plain", "en", "Session one begins.");
    append_item(out, first, "line-02-combining", "fr", combining);
    append_item(out, first, "line-03-emoji", "en", emoji);
    append_item(out, first, "line-04-explicit", "en", "First line\nSecond line");
    append_item(out, first, "line-05-short", "de", "Kurz und klar.");
    append_item(out, first, "line-06-spaces", "en", "  padded  ");
    append_item(out, first, "line-07-longword", "en", long_word);
    append_item(out, first, "line-08-explicit-overflow", "en",
                "One\nTwo\nThree\nFour");
    append_item(out, first, "line-09-wrap-overflow", "en", wrapped);
    append_item(out, first, "line-10-empty", "en", "");
    append_item(out, first, "line-11-missing", "en", missing);
    append_item(out, first, "line-12-literal", "en", literal);
    out << "\n  ]\n}\n";
    return out.str();
}
