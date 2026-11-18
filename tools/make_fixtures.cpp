#include "fixture_builder.hpp"

#include <cstring>
#include <iostream>
#include <map>
#include <string>
#include <vector>

using namespace mp4::fix;

namespace {

int usage(const char* prog) {
    std::cerr
        << "usage: " << prog << " --out DIR [--fault NAME|--all]\n"
        << "  NAME in: missing duplicate outoforder length drift encrypted\n"
        << "           truncated\n";
    return 2;
}

const std::map<std::string, Fault>& faults() {
    static const std::map<std::string, Fault> m = {
        {"missing", Fault::Missing},         {"duplicate", Fault::Duplicate},
        {"outoforder", Fault::OutOfOrder},   {"length", Fault::Length},
        {"drift", Fault::Drift},             {"encrypted", Fault::Encrypted},
        {"truncated", Fault::Truncated}};
    return m;
}

void emit_set(const std::string& dir,
              const std::vector<std::pair<std::string,
                                          std::vector<uint8_t>>>& files) {
    for (const auto& [name, bytes] : files)
        write_file(dir + "/" + name, bytes);
}

}  // namespace

int main(int argc, char** argv) {
    std::string out;
    std::string fault_name;
    bool all_faults = false;
    for (int i = 1; i < argc; ++i) {
        if (std::strcmp(argv[i], "--out") == 0 && i + 1 < argc)
            out = argv[++i];
        else if (std::strcmp(argv[i], "--fault") == 0 && i + 1 < argc)
            fault_name = argv[++i];
        else if (std::strcmp(argv[i], "--all") == 0)
            all_faults = true;
        else
            return usage(argv[0]);
    }
    if (out.empty()) return usage(argv[0]);
    if (!fault_name.empty() && all_faults) return usage(argv[0]);
    if (!fault_name.empty() && !faults().count(fault_name)) {
        std::cerr << "unknown fault: " << fault_name << "\n";
        return 2;
    }

    try {
        SampleSpec spec;
        write_file(out + "/input/init.mp4", build_init(spec));
        emit_set(out + "/input", build_normal_set(spec));
        std::cout << "normal set: 1 init + " << build_normal_set(spec).size()
                  << " fragments -> " << out << "/input\n";

        if (all_faults || !fault_name.empty()) {
            std::vector<std::pair<std::string, Fault>> selected;
            if (all_faults)
                for (const auto& [name, f] : faults()) selected.push_back({name, f});
            else
                selected.push_back({fault_name, faults().at(fault_name)});
            for (const auto& [name, f] : selected) {
                std::string dir = out + "/faults/" + name + "/input";
                write_file(dir + "/init.mp4", build_init(spec));
                emit_set(dir, build_fault_set(f, spec));
                std::cout << "fault set '" << name << "' -> " << dir << "\n";
            }
        }
    } catch (const std::exception& e) {
        std::cerr << "fixture generation failed: " << e.what() << "\n";
        return 4;
    }
    return 0;
}
