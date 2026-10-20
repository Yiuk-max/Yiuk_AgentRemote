# Additional clean files
cmake_minimum_required(VERSION 3.16)

if("${CONFIG}" STREQUAL "" OR "${CONFIG}" STREQUAL "")
  file(REMOVE_RECURSE
  "CMakeFiles\\yiuk-device-assist_autogen.dir\\AutogenUsed.txt"
  "CMakeFiles\\yiuk-device-assist_autogen.dir\\ParseCache.txt"
  "yiuk-device-assist_autogen"
  )
endif()
