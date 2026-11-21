#include "lut/cli.hpp"

#include <exception>
#include <iostream>
#include <vector>

#include "lut/cli_internal.hpp"

namespace lut {
namespace {

// Parses `--key value` pairs into a map. A bare `--flag` is stored as an
// empty value so boolean options can be detected by presence.
Options parseOptions(const std::vector<std::string>& args) {
    Options options;
    for (std::size_t i = 0; i < args.size(); ++i) {
        const std::string& arg = args[i];
        if (arg.rfind("--", 0) != 0 || arg.size() == 2) {
            throw UsageError("expected an option, got: " + arg);
        }
        const std::string key = arg.substr(2);
        if (i + 1 >= args.size() || args[i + 1].rfind("--", 0) == 0) {
            options.emplace(key, "");
        } else {
            if (!options.emplace(key, args[i + 1]).second) {
                throw UsageError("duplicate option: --" + key);
            }
            ++i;
        }
    }
    return options;
}

int classify(const LutException& error) {
    if (dynamic_cast<const UsageError*>(&error) != nullptr) {
        return kUsage;
    }
    if (dynamic_cast<const IoError*>(&error) != nullptr) {
        return kIoFailure;
    }
    // ParseError derives from LutDataError, so this covers both.
    return kInvalidLut;
}

}  // namespace

int runCommand(int argc, char** argv) {
    if (argc < 2) {
        std::cerr << "error=missing_command\n"
                  << "usage: lutctl {fixture-gen|bake|probe|render} [options]\n";
        return kUsage;
    }
    const std::string command = argv[1];
    std::vector<std::string> rest;
    for (int i = 2; i < argc; ++i) {
        rest.emplace_back(argv[i]);
    }

    try {
        const Options options = parseOptions(rest);
        if (command == "fixture-gen") {
            return cmdFixtureGen(options);
        }
        if (command == "bake") {
            return cmdBake(options);
        }
        if (command == "probe") {
            return cmdProbe(options);
        }
        if (command == "render") {
            return cmdRender(options);
        }
        throw UsageError("unknown command: " + command);
    } catch (const LutException& error) {
        std::cerr << "error=" << error.what() << '\n';
        return classify(error);
    } catch (const std::exception& error) {
        std::cerr << "error=" << error.what() << '\n';
        return kInvalidLut;
    }
}

}  // namespace lut
