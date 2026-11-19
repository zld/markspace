# CMake generated Testfile for 
# Source directory: /Users/zld/WorkSpace/pair-wise-workspace/14074
# Build directory: /Users/zld/WorkSpace/pair-wise-workspace/14074/build
# 
# This file includes the relevant testing commands required for 
# testing this directory and lists subdirectories to be tested as well.
add_test("core_unit_tests" "/Users/zld/WorkSpace/pair-wise-workspace/14074/build/test_core")
set_tests_properties("core_unit_tests" PROPERTIES  _CMAKE_TEST_BUILD_DEPENDS "/Users/zld/WorkSpace/pair-wise-workspace/14074/build/test_core" _BACKTRACE_TRIPLES "/Users/zld/WorkSpace/pair-wise-workspace/14074/CMakeLists.txt;91;add_test;/Users/zld/WorkSpace/pair-wise-workspace/14074/CMakeLists.txt;0;")
add_test("acceptance" "bash" "/Users/zld/WorkSpace/pair-wise-workspace/14074/tests/run_acceptance.sh" "/Users/zld/WorkSpace/pair-wise-workspace/14074/build")
set_tests_properties("acceptance" PROPERTIES  ENVIRONMENT "PYTHON=/opt/homebrew/Frameworks/Python.framework/Versions/3.15/bin/python3.15" WORKING_DIRECTORY "/Users/zld/WorkSpace/pair-wise-workspace/14074" _CMAKE_TEST_BUILD_DEPENDS "" _BACKTRACE_TRIPLES "/Users/zld/WorkSpace/pair-wise-workspace/14074/CMakeLists.txt;92;add_test;/Users/zld/WorkSpace/pair-wise-workspace/14074/CMakeLists.txt;0;")
