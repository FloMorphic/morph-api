package models

import (
	"strings"
	"testing"
)

func TestValidateReadSQL_Allows(t *testing.T) {
	cases := []string{
		"SELECT * FROM customers",
		"select id, name from customers where age > 30",
		"SELECT c.* FROM customers c JOIN orders o ON o.cust = c.id",
		"SELECT json_extract(data,'$.status') AS s FROM docs WHERE s = 'open'",
		"WITH recent AS (SELECT * FROM docs) SELECT * FROM recent",
		"SELECT * FROM t LIMIT 10",
		"  SELECT 1 FROM t ;  ", // single trailing ';' tolerated
	}
	for _, q := range cases {
		got, err := ValidateReadSQL(q)
		if err != nil {
			t.Errorf("expected allow, got error for %q: %v", q, err)
			continue
		}
		if got == "" {
			t.Errorf("expected non-empty query for %q", q)
		}
	}
}

func TestValidateReadSQL_InjectsLimit(t *testing.T) {
	got, err := ValidateReadSQL("SELECT * FROM t")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(strings.ToUpper(got), "LIMIT") {
		t.Errorf("expected a LIMIT to be injected, got %q", got)
	}
	// An author-supplied LIMIT must be left intact (not doubled).
	got, _ = ValidateReadSQL("SELECT * FROM t LIMIT 5")
	if strings.Count(strings.ToUpper(got), "LIMIT") != 1 {
		t.Errorf("expected the author's single LIMIT, got %q", got)
	}
}

func TestValidateReadSQL_Rejects(t *testing.T) {
	cases := map[string]string{
		"empty":              "   ",
		"not select":         "UPDATE t SET x = 1",
		"delete":             "DELETE FROM t",
		"drop":               "SELECT 1 FROM t; DROP TABLE t",
		"second statement":   "SELECT 1 FROM t; SELECT 2 FROM u",
		"pragma":             "PRAGMA table_info(t)",
		"attach":             "SELECT * FROM t WHERE x IN (ATTACH 'x')",
		"line comment":       "SELECT * FROM t -- comment",
		"block comment":      "SELECT * FROM t /* c */",
		"insert keyword":     "SELECT * FROM t WHERE insert = 1",
		"select into":        "SELECT * INTO u FROM t",
		"write via truncate": "SELECT 1 FROM t WHERE truncate",
	}
	for name, q := range cases {
		if _, err := ValidateReadSQL(q); err == nil {
			t.Errorf("%s: expected rejection, but %q was allowed", name, q)
		}
	}
}
