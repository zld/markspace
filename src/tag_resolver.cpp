#include "tag_resolver.hpp"

#include <cctype>
#include <cstdlib>

namespace asspipe {

namespace {

std::string lower(std::string s) {
    for (char& c : s)
        c = static_cast<char>(std::tolower(static_cast<unsigned char>(c)));
    return s;
}

bool starts_with_ci(const std::string& s, const std::string& prefix) {
    if (s.size() < prefix.size()) return false;
    for (std::size_t i = 0; i < prefix.size(); ++i)
        if (std::tolower(static_cast<unsigned char>(s[i])) !=
            static_cast<unsigned char>(prefix[i]))
            return false;
    return true;
}

// Split a block body into tag tokens at backslashes; a leading backslash
// is consumed. Empty tokens (empty block / doubled backslash) are kept.
std::vector<std::string> tokenize_block(const std::string& body) {
    std::vector<std::string> tokens;
    std::string cur;
    bool have = false; // text accumulated since the last backslash
    for (char ch : body) {
        if (ch == '\\') {
            if (have || !cur.empty()) tokens.push_back(cur);
            cur.clear();
            have = true;
            continue;
        }
        cur.push_back(ch);
    }
    if (have || !cur.empty()) tokens.push_back(cur);
    return tokens;
}

int int_or(const std::string& s, int fallback) {
    try {
        std::size_t used = 0;
        int v = std::stoi(s, &used);
        return used == 0 ? fallback : v;
    } catch (...) {
        return fallback;
    }
}

int hex_after_h(const std::string& s, std::size_t h_pos, int fallback) {
    if (h_pos == std::string::npos || h_pos + 2 > s.size()) return fallback;
    try {
        return static_cast<int>(
            std::stoul(s.substr(h_pos + 2), nullptr, 16) & 0xFFFFFFFFUL);
    } catch (...) {
        return fallback;
    }
}

bool all_digits_from(const std::string& s, std::size_t pos) {
    if (pos >= s.size()) return false;
    for (std::size_t i = pos; i < s.size(); ++i)
        if (std::isdigit(static_cast<unsigned char>(s[i])) == 0) return false;
    return true;
}

// True for \c, \1c..\4c, with or without an attached &H..& value.
bool is_colour_token(const std::string& t) {
    if (t.empty()) return false;
    std::size_t name_len;
    if (t[0] == 'c') name_len = 1;
    else if (t.size() >= 2 && t[0] >= '1' && t[0] <= '4' && t[1] == 'c')
        name_len = 2;
    else
        return false;
    return t.size() == name_len || t[name_len] == '&';
}

// True for \alpha, \1a..\4a, with or without an attached value.
bool is_alpha_token(const std::string& t) {
    if (t == "alpha" || starts_with_ci(t, "alpha&")) return true;
    if (t.size() >= 2 && t[0] >= '1' && t[0] <= '4' && t[1] == 'a')
        return t.size() == 2 || t[2] == '&';
    return false;
}

// Recognise the tag family of a token. Returns "" for unknown tokens.
// Recognition is strict about the shape so a malformed fragment inside an
// unclosed block stays literal text instead of being mis-parsed.
std::string recognise(const std::string& raw) {
    std::string t = lower(raw);
    if (t.empty()) return "";
    if (t[0] == 'r') return "r";
    if (t[0] == 'b' && (t.size() == 1 || all_digits_from(t, 1))) return "b";
    if (t[0] == 'i' && (t.size() == 1 || all_digits_from(t, 1))) return "i";
    if (starts_with_ci(t, "fs") && t.size() > 2) return "fs";
    if (starts_with_ci(t, "fn") && t.size() > 2) return "fn";
    if (is_colour_token(t)) return "c";
    if (is_alpha_token(t)) return "alpha";
    if (starts_with_ci(t, "pos")) return "pos";
    if (starts_with_ci(t, "an") && t.size() > 2) return "an";
    return "";
}

// Apply a recognised token. The family name comes from recognise().
void apply_known(const std::string& family, const std::string& raw,
                 TagCounts& counts, EffectiveState& state) {
    std::string t = lower(raw);
    if (family == "r") {
        counts.r++;
        state.style_name = raw.size() > 1 ? raw.substr(1) : std::string{};
        state.bold = state.italic = -1;
        state.fontsize = 0;
        state.fontname.clear();
        state.colour.clear();
        state.alpha = -1;
        state.alignment = 0;
    } else if (family == "b") {
        counts.b++;
        state.bold = t.size() == 1 ? 1 : int_or(t.substr(1), 0);
    } else if (family == "i") {
        counts.i++;
        state.italic = t.size() == 1 ? 1 : int_or(t.substr(1), 0);
    } else if (family == "fs") {
        counts.fs++;
        state.fontsize = int_or(t.substr(2), state.fontsize);
    } else if (family == "fn") {
        counts.fn++;
        state.fontname = raw.substr(2); // preserve original casing
    } else if (family == "c") {
        counts.c++;
        auto v = t.find('&');
        state.colour = v == std::string::npos ? std::string{} : t.substr(v);
    } else if (family == "alpha") {
        counts.alpha++;
        auto v = t.find('&');
        state.alpha = v == std::string::npos ? -1 : hex_after_h(t, v, -1);
    } else if (family == "pos") {
        counts.pos++;
        auto op = t.find('(');
        auto cl = t.find(')', op == std::string::npos ? 0 : op + 1);
        if (op != std::string::npos && cl != std::string::npos) {
            std::string args = t.substr(op + 1, cl - op - 1);
            auto comma = args.find(',');
            if (comma != std::string::npos) {
                state.has_pos = true;
                state.pos_x = int_or(args.substr(0, comma), state.pos_x);
                state.pos_y = int_or(args.substr(comma + 1), state.pos_y);
            }
        }
    } else if (family == "an") {
        counts.an++;
        int n = int_or(t.substr(2), 0);
        if (n >= 1 && n <= 9) state.alignment = n;
    }
}

} // namespace

EffectiveState resolve_tags(const std::string& text, TagCounts& counts,
                            bool& unclosed_block,
                            std::string& visible_text) {
    EffectiveState state;
    visible_text.clear();
    unclosed_block = false;

    std::size_t i = 0;
    while (i < text.size()) {
        if (text[i] != '{') {
            visible_text.push_back(text[i]); // visible text always kept
            ++i;
            continue;
        }
        auto close = text.find('}', i + 1);
        bool is_unclosed = close == std::string::npos;
        std::string body = is_unclosed
                               ? text.substr(i + 1)
                               : text.substr(i + 1, close - i - 1);

        if (is_unclosed) {
            // Conservative recovery: a token is consumed as a tag only when
            // it strictly matches a supported shape; any other fragment
            // (spaces, prose, half-written tags) stays visible, so text is
            // never silently swallowed.
            unclosed_block = true;
            std::size_t p = 0;
            std::string literal;
            auto flush = [&]() {
                if (!literal.empty()) {
                    visible_text += literal;
                    literal.clear();
                }
            };
            while (p < body.size()) {
                if (body[p] != '\\') {
                    // Leading prose (text before the first backslash) is
                    // literal; flush any pending tag literal first.
                    flush();
                    visible_text.push_back(body[p++]);
                    continue;
                }
                auto next = body.find('\\', p + 1);
                std::string segment = body.substr(
                    p + 1, next == std::string::npos
                                 ? std::string::npos
                                 : next - p - 1);
                std::string family = recognise(segment);
                if (!family.empty()) {
                    flush();
                    apply_known(family, segment, counts, state);
                } else {
                    literal.push_back('\\');
                    literal += segment;
                }
                if (next == std::string::npos) break;
                p = next;
            }
            flush();
            break;
        }

        // Well-formed block: unknown backslash tags are counted once but
        // do not change state; an empty block is a no-op reset marker.
        if (body.empty()) {
            counts.r++;
        } else {
            for (const auto& tok : tokenize_block(body)) {
                if (tok.empty()) continue;
                std::string family = recognise(tok);
                if (family.empty())
                    counts.unknown++;
                else
                    apply_known(family, tok, counts, state);
            }
        }
        i = close + 1;
    }
    return state;
}

EffectiveState resolve_event_tags(Event& event) {
    bool unclosed = false;
    std::string visible;
    EffectiveState state =
        resolve_tags(event.text, event.tags, unclosed, visible);
    event.unclosed_block = unclosed;
    return state;
}

} // namespace asspipe
