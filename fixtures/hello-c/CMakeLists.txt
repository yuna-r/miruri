cmake_minimum_required(VERSION 3.16)
project(miruri_hello C)

add_library(greeting STATIC src/greeting.c)
target_include_directories(greeting PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/src)

add_executable(miruri-hello src/main.c)
target_link_libraries(miruri-hello PRIVATE greeting)

install(TARGETS miruri-hello greeting
  RUNTIME DESTINATION bin
  ARCHIVE DESTINATION lib
  LIBRARY DESTINATION lib)
