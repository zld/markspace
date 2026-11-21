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

// Split a block body into tag tokens at backslashes. A leading backslash
// is consumed; an empty token (empty block / doubled backslash) is kept so
// callers can count empty blocks without losing surrounding text.
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

// Extract a parenthesised argument list, e.g. pos(12,34) -> "12,34".
// Returns "" and consumes nothing when the token is not of that shape.
std::string paren_arg(const std::string& token, const std::string& name) {
    if (!starts_with_ci(token, name)) return std::string{};
    std::size_t p = name.size();
    if (p >= token.size() || token[p] != '(') return std::string{};
    auto close = token.find(')', p + 1);
    if (close == std::string::npos) return std::string{};
    return token.substr(p + 1, close - p - 1);
}

// Apply one tag token. Unknown tags bump counts.unknown but never throw.
void apply_token(const std::string& raw, TagCounts& counts,
                 EffectiveState& state) {
    std::string token = lower(raw);
    if (token.empty()) return; // empty block: legal, nothing to apply

    if (token[0] == 'r') {
        // \r alone restores the base style; \rName restores a named one.
        counts.r++;
        state.style_name = token.size() > 1 ? raw.substr(1) : std::string{};
        state.bold = state.italic = -1;
        state.fontsize = 0;
        state.fontname.clear();
        state.colour.clear();
        state.alpha = -1;
        state.alignment = 0;
        return;
    }
    if (token[0] == 'b') {
        // Match \b, \b1, \b0 but not \blur / \be (non-numeric tails fall
        // through to the unknown-tag counter).
        bool numeric_tail = token.size() == 1;
        if (!numeric_tail) {
            numeric_tail = true;
            for (std::size_t i = 1; i < token.size(); ++i)
                if (!std::isdigit(static_cast<unsigned char>(token[i])))
                    numeric_tail = false;
        }
        if (numeric_tail) {
            counts.b++;
            state.bold = token.size() == 1 ? 1 : int_or(token.substr(1), 0);
            return;
        }
    }
    if (token[0] == 'i' &&
        (token.size() == 1 ||
         std::isdigit(static_cast<unsigned char>(token[1])))) {
        bool numeric_tail = token.size() == 1;
        if (!numeric_tail) {
            numeric_tail = true;
            for (std::size_t i = 1; i < token.size(); ++i)
                if (!std::isdigit(static_cast<unsigned char>(token[i])))
                    numeric_tail = false;
        }
        if (numeric_tail) {
            counts.i++;
            state.italic = token.size() == 1 ? 1 : int_or(token.substr(1), 0);
            return;
        }
    }
    if (starts_with_ci(token, "fs")) {
        counts.fs++;
        state.fontsize = int_or(token.substr(2), state.fontsize);
        return;
    }
    if (starts_with_ci(token, "fn")) {
        counts.fn++;
        state.fontname = raw.substr(2); // keep original casing of the name
        return;
    }
    // Colour: \c, \1c, \2c, \3c, \4c (with &H..& value, value optional).
    if (token == "c" || (token.size() >= 2 && token[0] >= '1' &&
                         token[0] <= '4' && token[1] == 'c')) {
        counts.c++;
        auto v = token.find('&');
        state.colour = v == std::string::npos ? std::string{} : token.substr(v);
        return;
    }
    if (token == "alpha" ||
        (token.size() >= 2 && token[0] >= '1' && token[0] <= '4' &&
         token.substr(1) == "a")) {
        counts.alpha++;
        auto v = token.find('&');
        state.alpha = v == std::string::npos ? -1 : int_or(token.substr(v + 2), -1);
        return;
    }
    std::string pos_arg = paren_arg(token, "pos");
    if (!pos_arg.empty() || starts_with_ci(token, "pos")) {
        counts.pos++;
        auto comma = pos_arg.find(',');
        if (comma != std::string::npos) {
            state.has_pos = true;
            state.pos_x = int_or(pos_arg.substr(0, comma), state.pos_x);
            state.pos_y = int_or(pos_arg.substr(comma + 1), state.pos_y);
        }
        return;
    }
    if (starts_with_ci(token, "an")) {
        counts.an++;
        int n = int_or(token.substr(2), 0);
        if (n >= 1 && n <= 9) state.alignment = n;
        return;
    }
    // Anything else (\q, \move, \fad, \blur, \k, ...) is reported once.
    counts.unknown++;
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
        char ch = text[i];
        if (ch != '{') {
            visible_text.push_back(ch); // visible text is always preserved
            ++i;
            continue;
        }
        auto close = text.find('}', i + 1);
        if (close == std::string::npos) {
            // Unclosed block: recover by treating the remainder as the body
            // and flag it; no text outside braces has been dropped.
            unclosed_block = true;
            std::string body = text.substr(i + 1);
            for (const auto& tok : tokenize_block(body))
                apply_token(tok, counts, state);
            break;
        }
        std::string body = text.substr(i + 1, close - i - 1);
        for (const auto& tok : tokenize_block(body))
            apply_token(tok, counts, state);
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
