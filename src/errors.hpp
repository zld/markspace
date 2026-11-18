// Common error types. Main maps these to process exit codes:
//   UsageError  -> exit 2 (bad parameters or invalid timeline)
//   RenderError -> exit 3 (rendering or file write failure)
#pragma once

#include <stdexcept>
#include <string>

namespace tr {

class UsageError : public std::runtime_error {
public:
  explicit UsageError(const std::string& msg) : std::runtime_error(msg) {}
};

class RenderError : public std::runtime_error {
public:
  explicit RenderError(const std::string& msg) : std::runtime_error(msg) {}
};

}  // namespace tr
