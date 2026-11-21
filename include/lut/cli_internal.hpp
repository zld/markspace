#ifndef LUT_CLI_INTERNAL_HPP
#define LUT_CLI_INTERNAL_HPP

#include <map>
#include <string>

#include "lut/cli.hpp"
#include "lut/model.hpp"
#include "lut/report.hpp"

namespace lut {

using Options = std::map<std::string, std::string>;

// Reads a mandatory --key value pair; throws UsageError when absent.
std::string requireOption(const Options& options, const std::string& key);

// Positive integer option (--size, --probe-grid, --tile-rows style).
int parsePositiveIntOption(const Options& options, const std::string& key,
                           int fallback);

// Non-negative floating point option with a default value.
double parseDoubleOption(const Options& options, const std::string& key,
                         double fallback);

// "r,g,b" encoded sample.
RGB parseRgbTriple(const std::string& text);

// --domain-policy {clamp,error}, defaulting to clamp.
DomainPolicy policyOption(const Options& options);

// Renders a 16-bucket histogram as [n0,n1,...,n15].
std::string formatHistogram(const Histogram& hist);

// Command handlers, one per lutctl subcommand.
int cmdFixtureGen(const Options& options);
int cmdBake(const Options& options);
int cmdProbe(const Options& options);
int cmdRender(const Options& options);

}  // namespace lut

#endif  // LUT_CLI_INTERNAL_HPP
