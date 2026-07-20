package repository

import (
	"crypto/rand"
	"encoding/binary"
	"strconv"
	"strings"
	"time"
)

// ID prefixes for each entity. Kept in sync with the web app's local ids
// (src/lib/id.ts uses `<prefix>_<...>`), so ids are interchangeable between the
// standalone/local mode and this backend.
const (
	WorkflowIDPrefix    = "flow"
	ContextIDPrefix     = "ctx"
	MemoryIDPrefix      = "mem"
	PromptIDPrefix      = "prompt"
	HumanTaskIDPrefix   = "hitl"
	NodeSettingIDPrefix = "nset"
	// Processes have no string id prefix — their identity is an auto-increment
	// integer index_id (see models.Process.IndexID).
)

// NewID returns a short, time-ordered, URL-safe id of the form
// `<prefix>_<base36 millis><base36 random>`, matching the web app's createId.
func NewID(prefix string) string {
	millis := time.Now().UnixMilli()
	var b [5]byte
	_, _ = rand.Read(b[:])
	randPart := binary.BigEndian.Uint64(append([]byte{0, 0, 0}, b[:]...))
	id := strconv.FormatInt(millis, 36) + strconv.FormatUint(randPart, 36)
	if prefix == "" {
		return id
	}
	return prefix + "_" + id
}

// HasPrefix reports whether id belongs to the given entity prefix (e.g. "ctx").
func HasPrefix(id, prefix string) bool {
	return strings.HasPrefix(id, prefix+"_")
}
