#pragma once

#include <stdexcept>

namespace tl {

// Error categories mapped to process exit codes in main.cpp:
//   ArgError  -> 2 (bad command line / invalid configuration)
//   DataError -> 3 (data or render validation failure)
//   IoError   -> 4 (file read/write failure)
struct ArgError : std::runtime_error {
  using std::runtime_error::runtime_error;
};
struct DataError : std::runtime_error {
  using std::runtime_error::runtime_error;
};
struct IoError : std::runtime_error {
  using std::runtime_error::runtime_error;
};

}  // namespace tl
