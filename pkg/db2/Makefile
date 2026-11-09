# Build helper for the cgo c-shared wrapper. The Python wheel build
# expects libadbc_driver_db2.{so,dylib,dll} produced by this Makefile.
#
# Targets:
#   make            — build the shared library for the current platform
#   make clean      — remove the shared library

ifeq ($(shell go env GOOS),linux)
	SUFFIX := so
else ifeq ($(shell go env GOOS),windows)
	SUFFIX := dll
else
	SUFFIX := dylib
endif

LIB := libadbc_driver_db2.$(SUFFIX)

.PHONY: all clean
all: $(LIB)

# scripts/build_lib.sh limits the exports to Adbc* (plus the deprecated
# Db2DriverInit alias) and sets the macOS deployment target.
$(LIB): driver.go init.go utils.c utils.h adbc.h exports_linux.map exports_darwin.txt exports_windows.def ../../go.mod ../../go.sum
	cd ../.. && scripts/build_lib.sh pkg/db2/$(LIB)

clean:
	rm -f $(LIB) libadbc_driver_db2.h
