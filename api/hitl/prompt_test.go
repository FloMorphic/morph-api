package hitlControllers

import (
	"reflect"
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// The snapshot a parked HITL node would have captured: a message stack an
// upstream LLM/MCP node built, plus the question it ended on.
func sampleData() map[string]any {
	return map[string]any{
		"question": "Approve the refund?",
		"messages": []any{
			map[string]any{"role": "assistant", "text": "Customer asked for a refund."},
			map[string]any{"role": "assistant", "text": "Policy allows it within 30 days."},
		},
		"order":  map[string]any{"id": "ord_42", "total": float64(120)},
		"urgent": true,
	}
}

func TestResolvePrompt(t *testing.T) {
	cases := []struct {
		name     string
		template string
		want     string
	}{
		{
			name:     "scalar variable inlines",
			template: "Please answer: {{$.question}}",
			want:     "Please answer: Approve the refund?",
		},
		{
			name:     "nested path",
			template: "Order {{$.order.id}} for {{$.order.total}}.",
			want:     "Order ord_42 for 120.",
		},
		{
			name:     "array index walks into the element",
			template: "First: {{$.messages[0].text}}",
			want:     "First: Customer asked for a refund.",
		},
		{
			name:     "boolean renders as a word",
			template: "urgent={{$.urgent}}",
			want:     "urgent=true",
		},
		{
			// An unresolvable variable is left visible rather than blanked: the
			// person can see the prompt was built on something that did not arrive.
			name:     "missing path is left as written",
			template: "Missing {{$.nope.deeper}} here",
			want:     "Missing {{$.nope.deeper}} here",
		},
		{
			name:     "no variables is passed through",
			template: "Just read this and answer.",
			want:     "Just read this and answer.",
		},
		{
			name:     "whitespace inside the braces is tolerated",
			template: "Q: {{ $.question }}",
			want:     "Q: Approve the refund?",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvePrompt(tc.template, sampleData()); got != tc.want {
				t.Fatalf("resolvePrompt() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A structured value is the whole point of a context ref — the message stack has
// to arrive readable, not as a Go-syntax dump.
func TestResolvePromptRendersStructuredValueAsJSON(t *testing.T) {
	got := resolvePrompt("{{$.messages}}", sampleData())
	want := `[
  {
    "role": "assistant",
    "text": "Customer asked for a refund."
  },
  {
    "role": "assistant",
    "text": "Policy allows it within 30 days."
  }
]`
	if got != want {
		t.Fatalf("resolvePrompt() = %q, want %q", got, want)
	}
}

func TestLookupPath(t *testing.T) {
	data := sampleData()
	cases := []struct {
		name string
		path string
		want any
		ok   bool
	}{
		{name: "bare $ is the whole snapshot", path: "$", want: data, ok: true},
		{name: "leading $ is optional", path: "question", want: "Approve the refund?", ok: true},
		{name: "index out of range misses", path: "$.messages[9]", ok: false},
		{name: "non-numeric index misses", path: "$.messages[first]", ok: false},
		{name: "indexing a non-array misses", path: "$.order[0]", ok: false},
		{name: "walking into a scalar misses", path: "$.question.text", ok: false},
		{name: "empty path misses", path: "  ", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lookupPath(data, tc.path)
			if ok != tc.ok {
				t.Fatalf("lookupPath(%q) ok = %v, want %v", tc.path, ok, tc.ok)
			}
			if tc.ok && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("lookupPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// resolveSession is what turns a recorded task into an openable conversation:
// the stored template stays intact and the rendering lands beside it.
func TestResolveSession(t *testing.T) {
	task := &models.HumanTask{
		Prompt: "Answer: {{$.question}}",
		Refs: []models.HumanTaskRef{
			{Name: "thread", Path: "$.messages"},
			{Name: "ghost", Path: "$.not.here"},
		},
		Data: sampleData(),
	}
	resolveSession(task)

	if task.Prompt != "Answer: {{$.question}}" {
		t.Fatalf("stored template was rewritten: %q", task.Prompt)
	}
	if task.PromptResolved != "Answer: Approve the refund?" {
		t.Fatalf("PromptResolved = %q", task.PromptResolved)
	}
	if task.Refs[0].Value == nil {
		t.Fatal("resolvable ref got no value")
	}
	if task.Refs[1].Value != nil {
		t.Fatalf("unresolvable ref got a value: %v", task.Refs[1].Value)
	}
}

// A task recorded with no prompt (questions only) must stay that way — an empty
// template must not produce an empty "resolved" string the UI would render as a
// blank opening turn.
func TestResolveSessionLeavesEmptyPromptAlone(t *testing.T) {
	task := &models.HumanTask{Data: sampleData()}
	resolveSession(task)
	if task.PromptResolved != "" {
		t.Fatalf("PromptResolved = %q, want empty", task.PromptResolved)
	}
}
