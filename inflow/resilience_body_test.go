package inflow

import (
	"reflect"
	"testing"
)

// The LLM and HTTP plugins read the call deadline and the retry count off
// body.settings, and the compiler whitelists the keys it projects — so a key
// missing here never reaches the plugin at all, whatever the settings form
// saved.
//
// The retry count is a POINTER on the plugin side, which makes absent and zero
// two different instructions. The projection has to preserve that distinction:
// flattening an absent key to 0 would read as "never retry" and quietly undo
// the default on every profile that predates the field.
func TestResilienceKnobsReachThePlugin(t *testing.T) {
	t.Run("an explicit zero travels as zero", func(t *testing.T) {
		got := llmSettingsBody(map[string]any{
			"provider": "openai", "model": "gpt-4o",
			"request_timeout_s": float64(240), "max_retries": float64(0),
		})
		if got["request_timeout_s"] != 240 {
			t.Errorf("request_timeout_s = %#v, want 240", got["request_timeout_s"])
		}
		if got["max_retries"] != 0 {
			t.Errorf("max_retries = %#v, want an explicit 0", got["max_retries"])
		}
	})

	t.Run("an absent knob is omitted, not zeroed", func(t *testing.T) {
		llm := llmSettingsBody(map[string]any{"provider": "openai", "model": "gpt-4o"})
		if _, present := llm["max_retries"]; present {
			t.Errorf("max_retries was shipped as %#v — the plugin default is now unreachable", llm["max_retries"])
		}
		if _, present := llm["request_timeout_s"]; present {
			t.Errorf("request_timeout_s was shipped as %#v, want it omitted", llm["request_timeout_s"])
		}

		http := httpSettingsBody(map[string]any{"base_url": "https://api.example.com"})
		if _, present := http["max_retries"]; present {
			t.Errorf("max_retries was shipped as %#v — a POST node would stop retrying", http["max_retries"])
		}
	})

	t.Run("a blank or unparseable value counts as absent", func(t *testing.T) {
		for _, raw := range []any{"", "   ", "off", nil, true} {
			got := httpSettingsBody(map[string]any{"max_retries": raw})
			if _, present := got["max_retries"]; present {
				t.Errorf("max_retries %#v was shipped as %#v, want it omitted", raw, got["max_retries"])
			}
		}
	})

	t.Run("a form-serialised number is read", func(t *testing.T) {
		got := httpSettingsBody(map[string]any{"max_retries": "5"})
		if got["max_retries"] != 5 {
			t.Errorf("max_retries = %#v, want 5", got["max_retries"])
		}
	})

	// Nothing else from the profile travels, and the existing contract fields
	// are untouched by the addition.
	t.Run("the rest of the contract is unchanged", func(t *testing.T) {
		got := llmSettingsBody(map[string]any{
			"provider": "openai", "url": "https://api.openai.com/v1", "model": "gpt-4o",
			"access_token": "sk", "temperature": 0.7, "max_tokens": float64(0),
			"unrelated": "dropped",
		})
		want := map[string]any{
			"provider": "openai", "url": "https://api.openai.com/v1", "model": "gpt-4o",
			"access_token": "sk", "temperature": 0.7, "max_tokens": 0,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("llmSettingsBody = %#v, want %#v", got, want)
		}
	})
}
