#include "style_graph.hpp"

#include <functional>

namespace subaudit {

bool resolve_styles(const Document& doc, std::vector<ResolvedStyle>& out,
                    std::string& err) {
    const std::size_t n = doc.styles.size();
    std::map<std::string, std::size_t> index;
    for (std::size_t i = 0; i < n; ++i) {
        const Style& s = doc.styles[i];
        const auto it = index.find(s.name);
        if (it != index.end()) {
            err = "duplicate style name '" + s.name + "' (lines " +
                  std::to_string(doc.styles[it->second].line) + " and " +
                  std::to_string(s.line) + ")";
            return false;
        }
        index[s.name] = i;
    }

    out.assign(n, ResolvedStyle{});
    std::vector<int> state(n, 0);  // 0 = unvisited, 1 = on stack, 2 = done
    std::function<bool(std::size_t)> visit = [&](std::size_t i) -> bool {
        if (state[i] == 2) return true;
        const Style& s = doc.styles[i];
        if (state[i] == 1) {
            err = "style inheritance cycle at '" + s.name + "' (line " +
                  std::to_string(s.line) + ")";
            return false;
        }
        state[i] = 1;
        ResolvedStyle rs;
        rs.name = s.name;
        std::map<std::string, std::string> inherited;
        const auto pit = s.fields.find("Parent");
        const std::string parent = pit != s.fields.end() ? pit->second : "";
        if (!parent.empty() && parent != "~") {
            const auto pi = index.find(parent);
            if (pi == index.end()) {
                err = "line " + std::to_string(s.line) + ": style '" + s.name +
                      "' inherits from unknown parent '" + parent + "'";
                return false;
            }
            if (!visit(pi->second)) return false;
            inherited = out[pi->second].fields;
            rs.chain = out[pi->second].chain;
        }
        rs.chain.push_back(s.name);
        rs.fields = inherited;
        for (const auto& kv : s.fields) {
            if (kv.first == "Name" || kv.first == "Parent") continue;
            if (kv.second == "~") {
                if (inherited.find(kv.first) == inherited.end()) {
                    err = "line " + std::to_string(s.line) + ": field '" + kv.first +
                          "' of style '" + s.name +
                          "' is '~' but no parent defines it";
                    return false;
                }
                // inherited value already present in rs.fields
            } else {
                rs.fields[kv.first] = kv.second;
            }
        }
        out[i] = std::move(rs);
        state[i] = 2;
        return true;
    };
    for (std::size_t i = 0; i < n; ++i)
        if (!visit(i)) return false;
    return true;
}

}  // namespace subaudit
