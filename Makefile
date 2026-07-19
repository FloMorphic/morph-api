# FloMorphic API — build/run helpers.
#
# The sqlite backend links mattn/go-sqlite3 (cgo) and compiles the sqlite-vec
# extension, whose header includes "sqlite3.h". We vendor a version-matched copy
# under repository/sqlite/cdeps and point cgo at it here, so a plain `make build`
# works without installing libsqlite3-dev.

BINARY      := flomorphic-api
CDEPS_DIR   := $(CURDIR)/repository/sqlite/cdeps
export CGO_ENABLED := 1
export CGO_CFLAGS  := -I$(CDEPS_DIR)

.PHONY: build run test tidy sqlc clean

build: ## Compile the server binary
	go build -o $(BINARY) .

run: ## Run the server
	go run .

test: ## Run tests
	go test ./...

tidy: ## Sync go.mod / go.sum
	go mod tidy

sqlc: ## Regenerate sqlc code from schema + queries
	sqlc generate

clean:
	rm -f $(BINARY)
