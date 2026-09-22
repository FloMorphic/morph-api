package inflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The jev plugin reads body.questions as its exact Question wire shape, so the
// lowering has to hold for BOTH shapes a node's `questions` field arrives in:
// []any of map[string]any for a flow loaded from the store (it round-trips
// through JSON), and []map[string]any for one compiled in memory straight after
// a designer patch (flo_plan_patch, which never round-trips). Frontend-only row
// ids are dropped, `route` travels only when it is off (the plugin defaults it
// on) and `min_confidence` only when set.
func TestJevQuestionsLowering(t *testing.T) {
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
		if got := jevQuestions(data); !reflect.DeepEqual(got, want) {
			t.Errorf("jevQuestions =\n%#v\nwant\n%#v", got, want)
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
		if got := jevQuestions(map[string]any{"questions": typed}); !reflect.DeepEqual(got, want) {
			t.Errorf("jevQuestions =\n%#v\nwant\n%#v", got, want)
		}
	})
}

// The settings profile is projected onto the plugin's JevSettings contract, and
// nothing else from the profile travels.
func TestJevSettingsBody(t *testing.T) {
	got := jevSettingsBody(map[string]any{
		"access_token": "sk_test", "model": "jev-latest", "timeout_seconds": float64(30),
		"unrelated": "dropped",
	})
	want := map[string]any{"access_token": "sk_test", "model": "jev-latest", "url": "", "timeout_seconds": 30}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("jevSettingsBody = %#v, want %#v", got, want)
	}
}
