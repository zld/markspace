#include "inline_tags.hpp"

#include <cctype>
#include <cstdio>
#include <cstdlib>
#include <cstring>

namespace sp {

int TagCounts::total() const {
    return bold + italic + underline + strikeout + font_name + font_size + colour + alpha +
           outline + shadow + alignment + position + move + margin_l + margin_r + margin_v +
           reset + reset_style + scale_x + scale_y + spacing + rotation + karaoke + clip +
           drawing + line_break + hard_space + unknown;
}

std::string TagCounts::summary() const {
    struct Entry { const char* key; int value; };
    const Entry entries[] = {
        {"b", bold},         {"i", italic},       {"u", underline},
        {"s", strikeout},    {"fn", font_name},   {"fs", font_size},
        {"c", colour},       {"alpha", alpha},    {"bord", outline},
        {"shad", shadow},    {"an", alignment},   {"pos", position},
        {"move", move},      {"margins", margin_l + margin_r + margin_v},
        {"r", reset + reset_style},                {"scale", scale_x + scale_y},
        {"fsp", spacing},    {"rot", rotation},   {"k", karaoke},
        {"clip", clip},      {"p", drawing},      {"br", line_break},
        {"h", hard_space},   {"unknown", unknown},
    };
    std::string out;
    for (const Entry& entry : entries) {
        if (entry.value == 0) continue;
        if (!out.empty()) out += ",";
        out += entry.key;
        out += "=";
        out += std::to_string(entry.value);
    }
    return out.empty() ? "none" : out;
}

static void json_escape_into(std::string& out, const std::string& text) {
    for (unsigned char ch : text) {
        switch (ch) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (ch < 0x20) {
                    char buf[8];
                    std::snprintf(buf, sizeof buf, "\\u%04x", ch);
                    out += buf;
                } else {
                    out += static_cast<char>(ch);
                }
        }
    }
}

std::string TagCounts::to_json() const {
    char buf[1024];
    std::snprintf(buf, sizeof buf,
                  "{\"bold\":%d,\"italic\":%d,\"underline\":%d,\"strikeout\":%d,\"font_name\":%d,"
                  "\"font_size\":%d,\"colour\":%d,\"alpha\":%d,\"outline\":%d,\"shadow\":%d,"
                  "\"alignment\":%d,\"position\":%d,\"move\":%d,\"margin_l\":%d,\"margin_r\":%d,"
                  "\"margin_v\":%d,\"reset\":%d,\"reset_style\":%d,\"scale_x\":%d,\"scale_y\":%d,"
                  "\"spacing\":%d,\"rotation\":%d,\"karaoke\":%d,\"clip\":%d,\"drawing\":%d,"
                  "\"line_break\":%d,\"hard_space\":%d,\"unknown\":%d}",
                  bold, italic, underline, strikeout, font_name, font_size, colour, alpha,
                  outline, shadow, alignment, position, move, margin_l, margin_r, margin_v,
                  reset, reset_style, scale_x, scale_y, spacing, rotation, karaoke, clip,
                  drawing, line_break, hard_space, unknown);
    std::string out(buf);
    (void)json_escape_into;
    return out;
}

namespace {

struct TagToken {
    std::string name;   // lower-case, without the leading backslash
    std::string arg;    // raw argument text
    bool has_parens = false;
};

// Known ASS override tag names, longest first so that \fscx wins over \fs.
// Each entry is (name, argument kind): 0 = numeric/parenthesised, 1 = string.
struct KnownTag {
    const char* name;
    bool string_arg;
};
const KnownTag kKnownTags[] = {
    {"alpha", false}, {"xbord", false}, {"ybord", false}, {"xshad", false}, {"yshad", false},
    {"iclip", false}, {"fscx", false},  {"fscy", false},  {"bord", false},  {"shad", false},
    {"blur", false},  {"fade", false},  {"clip", false},  {"move", false},  {"fade", false},
    {"frz", false},   {"frx", false},   {"fry", false},   {"fax", false},   {"fay", false},
    {"pbo", false},   {"kf", false},    {"ko", false},    {"kt", false},    {"an", false},
    {"fn", true},     {"fs", false},    {"fe", false},    {"fr", false},    {"fsp", false},
    {"fad", false},   {"be", false},    {"org", false},   {"1c", false},    {"2c", false},
    {"3c", false},    {"4c", false},    {"1a", false},    {"2a", false},    {"3a", false},
    {"4a", false},    {"b", false},     {"i", false},     {"u", false},     {"s", false},
    {"c", false},     {"a", false},     {"p", false},     {"q", false},     {"r", true},
    {"t", false},     {"k", false},
};

bool starts_with_lower(const std::string& body, size_t pos, const char* name) {
    size_t len = std::strlen(name);
    if (pos + len > body.size()) return false;
    for (size_t i = 0; i < len; ++i) {
        if (std::tolower(static_cast<unsigned char>(body[pos + i])) != name[i]) return false;
    }
    return true;
}

// A numeric tag argument starts with a digit, sign, '&' (colour) or '('.
bool plausible_numeric_arg(const std::string& body, size_t pos) {
    if (pos >= body.size()) return true;
    char ch = body[pos];
    return std::isdigit(static_cast<unsigned char>(ch)) != 0 || ch == '-' || ch == '+' ||
           ch == '(' || ch == '&';
}

TagToken parse_tag(const std::string& body, size_t& pos) {
    TagToken token;
    // Tag names are not delimited: \fnArial means name "fn", arg "Arial".
    // Only split off a known name when what follows can be its argument, so
    // \foobar or \unknownTag is reported as one unknown tag instead of being
    // misread as \f / \u plus text.
    for (const KnownTag& candidate : kKnownTags) {
        if (!starts_with_lower(body, pos, candidate.name)) continue;
        size_t after = pos + std::strlen(candidate.name);
        if (candidate.string_arg || plausible_numeric_arg(body, after)) {
            token.name = candidate.name;
            pos = after;
            break;
        }
    }
    if (token.name.empty()) {
        while (pos < body.size() && std::isalnum(static_cast<unsigned char>(body[pos]))) {
            token.name += static_cast<char>(std::tolower(static_cast<unsigned char>(body[pos])));
            ++pos;
        }
    }
    if (pos < body.size() && body[pos] == '(') {
        // Balanced parentheses: \clip(0,0,10,10), \t(\fs20), nested forms.
        int depth = 0;
        size_t start = pos + 1;
        size_t close_at = std::string::npos;
        while (pos < body.size()) {
            if (body[pos] == '(') ++depth;
            else if (body[pos] == ')') {
                --depth;
                if (depth == 0) { close_at = pos; ++pos; break; }
            }
            ++pos;
        }
        token.has_parens = true;
        token.arg = close_at == std::string::npos ? body.substr(start)
                                                  : body.substr(start, close_at - start);
    } else if (token.name == "fn" || token.name == "r") {
        // String arguments run to the next tag or the end of the block.
        size_t start = pos;
        while (pos < body.size() && body[pos] != '\\') ++pos;
        token.arg = body.substr(start, pos - start);
    } else {
        size_t start = pos;
        while (pos < body.size() && body[pos] != '\\' && body[pos] != ')') ++pos;
        token.arg = body.substr(start, pos - start);
    }
    return token;
}

void count_token(TagCounts& counts, const TagToken& token, const std::string& arg, bool& known) {
    const std::string& n = token.name;
    known = true;
    if (n == "b") ++counts.bold;
    else if (n == "i") ++counts.italic;
    else if (n == "u") ++counts.underline;
    else if (n == "s") ++counts.strikeout;
    else if (n == "fn") ++counts.font_name;
    else if (n == "fs") ++counts.font_size;
    else if (n == "c" || n == "1c" || n == "2c" || n == "3c" || n == "4c") ++counts.colour;
    else if (n == "alpha" || n == "1a" || n == "2a" || n == "3a" || n == "4a") ++counts.alpha;
    else if (n == "bord" || n == "xbord" || n == "ybord") ++counts.outline;
    else if (n == "shad" || n == "xshad" || n == "yshad") ++counts.shadow;
    else if (n == "an" || n == "a") ++counts.alignment;
    else if (n == "pos") ++counts.position;
    else if (n == "move") ++counts.move;
    else if (n == "q" || n == "fscx" || n == "fscy" || n == "fsp" || n == "fr" || n == "frz" ||
             n == "frx" || n == "fry" || n == "fax" || n == "fay" || n == "be" || n == "blur" ||
             n == "fe" || n == "fad" || n == "fade" || n == "org" || n == "pbo" || n == "p") {
        if (n == "fscx") ++counts.scale_x;
        else if (n == "fscy") ++counts.scale_y;
        else if (n == "fsp") ++counts.spacing;
        else if (n == "fr" || n == "frz" || n == "frx" || n == "fry") ++counts.rotation;
        else if (n == "p") ++counts.drawing;
    } else if (n == "k" || n == "kf" || n == "ko" || n == "kt") ++counts.karaoke;
    else if (n == "clip" || n == "iclip") ++counts.clip;
    else if (n == "r") {
        if (trim_ascii(arg).empty()) ++counts.reset;
        else ++counts.reset_style;
    } else if (n == "t") {
        // \t(...) animation: its argument is itself a tag list; count unknown
        // names inside it as unknown tags but do not recurse into \t nesting.
        known = true;
    } else {
        known = false;
        ++counts.unknown;
    }
}

std::string normalise_tag_value(const std::string& name, const std::string& raw) {
    std::string arg = trim_ascii(raw);
    if (name == "fn") return arg.empty() ? "<reset>" : arg;
    return arg;
}

}  // namespace

InlineParse parse_inline_tags(const std::string&, int, int) {
    return InlineParse{};
}

std::string canonicalize_for_render(const std::string& raw_text) {
    return raw_text;
}

}  // namespace sp
