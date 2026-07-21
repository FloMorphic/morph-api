package models

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Read-query guard limits. A read is a single, bounded SELECT — these cap the
// work a query can ask for before it is ever handed to the database.
const (
	// MaxReadQueryLen bounds the SQL text so the validator's own scanning work
	// is bounded and a pathological query can't be used to stall the handler.
	MaxReadQueryLen = 4000
	// MaxReadRows is the row cap injected when a query carries no LIMIT of its
	// own, so an unqualified SELECT can never stream an unbounded result set.
	MaxReadRows = 1000
)

// forbiddenKeywords block anything that writes or changes state. This is a
// standalone system: the user may read *any* table, join freely, and use
// subqueries — so nothing about which tables a read touches is restricted. What
// stays refused is mutation and engine-level escapes, so a "read" can never
// change data or reach outside a read. (A single SELECT is read-only in SQLite
// by nature; this list plus the single-statement rule is belt-and-suspenders.)
var forbiddenKeywords = []string{
	"insert", "update", "delete", "drop", "alter", "create", "replace",
	"truncate", "merge", "upsert", "into",
	"attach", "detach", "pragma", "vacuum", "reindex", "trigger",
	"grant", "revoke", "load_extension", "writefile",
}

// identifierRe matches a bare, unqualified SQL identifier: a table/column name
// with no schema prefix, no quoting, no punctuation.
var identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsSafeIdentifier reports whether s is a plain SQL identifier safe to embed in
// DDL/DML that cannot be parameterized (a table name). Used both when a document
// store is created (to reject an unusable table name up front) and when its
// table name is interpolated into a statement.
func IsSafeIdentifier(s string) bool {
	return identifierRe.MatchString(s)
}

var wordCache = map[string]*regexp.Regexp{}

// containsWord reports whether word appears in lower as a whole SQL token.
func containsWord(lower, word string) bool {
	re, ok := wordCache[word]
	if !ok {
		re = regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)
		wordCache[word] = re
	}
	return re.MatchString(lower)
}

// ValidateReadSQL is the gate a store read query passes through before it can
// touch the database. It returns a query safe to execute or an error explaining
// the rejection; a rejected query MUST NOT be run.
//
// This is a standalone system, so a read may target any table and use joins and
// subqueries — table scoping is intentionally NOT enforced. The guarantees are:
//
//   - single statement — no ';'-chained second query;
//   - read-only — must be a SELECT (or WITH … SELECT); every write/DDL and
//     engine-escape keyword (INSERT/UPDATE/DROP/PRAGMA/ATTACH/…) is refused;
//   - opaque — comments are refused so nothing can be smuggled past the scan
//     or hide a statement separator;
//   - bounded — the SQL length and the returned row count are both capped.
func ValidateReadSQL(query string) (string, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", errors.New("read query is empty")
	}
	if len(q) > MaxReadQueryLen {
		return "", fmt.Errorf("read query is too long (max %d characters)", MaxReadQueryLen)
	}
	// Control characters (NUL, etc.) have no place in a read query and can throw
	// off downstream scanning — refuse them.
	if strings.ContainsRune(q, '\x00') {
		return "", errors.New("read query contains a control character")
	}
	// No comments: `--`, `/* */`, and `#` can hide a statement separator or a
	// write keyword from the scan below, so any comment marker is a hard reject.
	if strings.Contains(q, "--") || strings.Contains(q, "/*") || strings.Contains(q, "*/") || strings.Contains(q, "#") {
		return "", errors.New("comments are not allowed in a read query")
	}
	// Single statement only: allow one optional trailing ';', reject any other.
	q = strings.TrimRight(q, "; \t\r\n")
	if strings.Contains(q, ";") {
		return "", errors.New("only a single statement is allowed in a read query")
	}

	lower := strings.ToLower(q)
	// A read must be a SELECT — or a WITH…SELECT (CTEs are read-only). This alone
	// rules out PRAGMA/ATTACH/writes, which are their own statement forms.
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return "", errors.New("only SELECT read queries are allowed")
	}
	for _, kw := range forbiddenKeywords {
		if containsWord(lower, kw) {
			return "", fmt.Errorf("%q is not allowed in a read query", strings.ToUpper(kw))
		}
	}

	// Bound the result set when the author did not.
	if !containsWord(lower, "limit") {
		q = fmt.Sprintf("%s LIMIT %d", q, MaxReadRows)
	}
	return q, nil
}
