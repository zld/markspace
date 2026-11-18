#include "rewriter.hpp"
namespace mp4 {
// Real implementations arrive in the following patches.
RewriteResult rewrite_package(const MediaPackage&, const CompatReport&,
                              const RewriteOptions&) {
    throw RewriteError("rewriter not yet available");
}
WriteOutcome write_package_atomic(const RewriteResult&, const std::string&,
                                  const Limits&) {
    throw RewriteError("writer not yet available");
}
}  // namespace mp4
