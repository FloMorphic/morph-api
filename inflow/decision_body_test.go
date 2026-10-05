package inflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The ai-decision plugin reads body.questions as its exact Question wire shape, so the
// lowering has to hold for BOTH shapes a node's `questions` field arrives in:
// []any of map[string]any for a flow loaded from the store (it round-trips
// through JSON), and []map[string]any for one compiled in memory straight after
// a designer patch (flo_plan_patch, which never round-trips). Frontend-only row
// ids are dropped, `route` travels only when it is off (the plugin defaults it
// on) and `min_confidence` only when set.
func TestDecisionQuestionsLowering(t *testing.T) {
	const raw = `{
      "questions": [
        {
          "id": "category", "type": "choice", "instructions": "Which team?", "route": true,
          "options": [
            { "id": "opt-1", "name": "billing", "description": "Payments" },
            { "id": "opt-2", "name": "other", "description": "None of the above" },
            { "id": "opt-3", "description": "no name — dropped" }
          ]
        },
        {
          "id": "urgency", "type": "score", "instructions": "How urgent?", "route": false,
          "min_confidence": 0.8,
          "options": [{ "id": "opt-4", "name": "low" }, { "id": "opt-5", "name": "high" }]
        },
        { "type": "noul", "instructions": "no id — dropped", "options": [] }
      ]
    }`

	want := []map[string]any{
		{
			"id": "category", "type": "choice", "instructions": "Which team?",
			"options": []map[string]any{
				{"name": "billing", "description": "Payments"},
				{"name": "other", "description": "None of the above"},
			},
		},
		{
			"id": "urgency", "type": "score", "instructions": "How urgent?",
			"route": false, "min_confidence": 0.8,
			"options": []map[string]any{
				{"name": "low", "description": ""},
				{"name": "high", "description": ""},
			},
		},
	}

	t.Run("loaded from the store (JSON round-trip)", func(t *testing.T) {
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			t.Fatal(err)
		}
		if got := decisionQuestions(data); !reflect.DeepEqual(got, want) {
			t.Errorf("decisionQuestions =\n%#v\nwant\n%#v", got, want)
		}
	})

	t.Run("straight from a designer patch (no round-trip)", func(t *testing.T) {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatal(err)
		}
		// Re-type the rows the way designer.PlanPatch leaves them.
		rows := decoded["questions"].([]any)
		typed := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			q := r.(map[string]any)
			if opts, ok := q["options"].([]any); ok {
				o := make([]map[string]any, 0, len(opts))
				for _, x := range opts {
					o = append(o, x.(map[string]any))
				}
				q["options"] = o
			}
			typed = append(typed, q)
		}
		if got := decisionQuestions(map[string]any{"questions": typed}); !reflect.DeepEqual(got, want) {
			t.Errorf("decisionQuestions =\n%#v\nwant\n%#v", got, want)
		}
	})
}

// The settings profile is projected onto the plugin's DecisionSettings contract, and
// nothing else from the profile travels.
func TestDecisionSettingsBody(t *testing.T) {
	got := decisionSettingsBody(map[string]any{
		"access_token": "sk_test", "model": "jev-latest", "timeout_seconds": float64(30),
		"unrelated": "dropped",
	})
	want := map[string]any{"access_token": "sk_test", "model": "jev-latest", "url": "", "timeout_seconds": 30}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("jevSettingsBody = %#v, want %#v", got, want)
	}
}

// The API takes a question's instructions as a string OR as structured
// guidance — the question in one field and the data it cites by backticked name
// in the others. The lowering used to flatten the field with getStr, which
// turned the structured form into "" and lost the reference data silently, so
// this pins the pass-through.
func TestInstructionsOf(t *testing.T) {
	structured := map[string]any{"question": "breaches `policy`?", "policy": "{{$.kb.policy}}"}
	for _, c := range []struct {
		name string
		in   any
		want any
	}{
		{"plain question", "Which team?", "Which team?"},
		{"structured object", structured, structured},
		{"array form", []any{"q", "data"}, []any{"q", "data"}},
		{"missing", nil, ""},
		{"wrong type", 7, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := instructionsOf(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v want %#v", got, c.want)
			}
		})
	}
}

// Evidence rows lower to the plugin's {source, text} wire shape with the
// frontend row ids dropped, and a row with no text is skipped — it would only
// spend the model's context budget.
func TestDecisionEvidenceLowering(t *testing.T) {
	const raw = `{
      "evidence": [
        { "id": "ev-1", "source": "contract.pdf", "text": "Section 12 allows termination..." },
        { "id": "ev-2", "source": "reg.pdf", "text": "{{$.kb.regulation}}" },
        { "id": "ev-3", "source": "empty.pdf", "text": "   " },
        { "id": "ev-4", "text": "no source is fine" }
      ]
    }`
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	got := decisionEvidence(data)
	want := []map[string]any{
		{"source": "contract.pdf", "text": "Section 12 allows termination..."},
		{"source": "reg.pdf", "text": "{{$.kb.regulation}}"},
		{"source": "", "text": "no source is fine"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

// The compiled body is the plugin's RunBody contract. Two things matter here:
// evidence rows reach it under `evidence`, and a node with none compiles to the
// body it compiled to before evidence existed — the compatibility promise for
// every already-saved flow.
func TestDecisionBodyCarriesEvidence(t *testing.T) {
	data := map[string]any{
		"settings": map[string]any{"access_token": "k", "model": "jev-latest"},
		"body":     map[string]any{"state": "{{$.ticket}}"},
		"questions": []any{
			map[string]any{"id": "category", "type": "choice", "instructions": "Which team?",
				"options": []any{map[string]any{"name": "billing"}}},
		},
	}

	body := decisionBody(data)
	if _, present := body["evidence"]; present {
		t.Fatal("a node with no evidence rows must not grow an evidence field")
	}
	if body["state"] != "{{$.ticket}}" {
		t.Fatalf("state should ship from body: %#v", body["state"])
	}

	data["evidence"] = []any{map[string]any{"id": "ev-1", "source": "contract.pdf", "text": "Section 12..."}}
	body = decisionBody(data)
	rows, ok := body["evidence"].([]map[string]any)
	if !ok || len(rows) != 1 || rows[0]["source"] != "contract.pdf" {
		t.Fatalf("evidence not in the body: %#v", body["evidence"])
	}
	if body["state"] != "{{$.ticket}}" {
		t.Fatalf("state should still ship from body: %#v", body["state"])
	}
}

// The drawer expresses the API's structured instructions as two readable
// halves: the question as text, and the data it cites as named `references`
// rows. They are assembled here, since the wire shape is the compiler's job.
func TestInstructionsWithReferences(t *testing.T) {
	t.Run("no references keeps the plain string", func(t *testing.T) {
		got := instructionsWithReferences(map[string]any{"instructions": "Which team?"})
		if got != "Which team?" {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("references become the structured form", func(t *testing.T) {
		got := instructionsWithReferences(map[string]any{
			"instructions": "Does this breach `policy`?",
			"references": []any{
				map[string]any{"id": "r-1", "name": "policy", "value": "{{$.kb.policy}}"},
				map[string]any{"id": "r-2", "name": "", "value": "dropped: unnamed"},
				map[string]any{"id": "r-3", "name": "question", "value": "dropped: reserved"},
			},
		})
		want := map[string]any{"question": "Does this breach `policy`?", "policy": "{{$.kb.policy}}"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v want %#v", got, want)
		}
	})
	t.Run("an already-structured instructions wins", func(t *testing.T) {
		structured := map[string]any{"question": "q", "data": "d"}
		got := instructionsWithReferences(map[string]any{
			"instructions": structured,
			"references":   []any{map[string]any{"name": "policy", "value": "x"}},
		})
		if !reflect.DeepEqual(got, structured) {
			t.Fatalf("got %#v", got)
		}
	})
}
