# cdeps — vendored C header

`sqlite3.h` here is the SQLite public API header (public domain), vendored so the
cgo build of the [sqlite-vec](https://github.com/asg017/sqlite-vec) extension can
resolve its `#include "sqlite3.h"`.

It is kept version-matched with the SQLite that `mattn/go-sqlite3` bundles and
links, so the declarations here and the symbols linked into the binary agree.

The build points cgo at this directory via `CGO_CFLAGS=-I.../cdeps` — see the
`Makefile`. Contributors who prefer to use the system SQLite headers can instead
install `libsqlite3-dev` and drop the flag.
