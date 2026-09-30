package sqlite

import (
	"context"
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// The Telegram binding is the only part of a task stored as JSON in its own
// column, and the bridge depends on two halves of it round-tripping for different
// reasons: the design-time binding is how it reaches the right bot, and the cursor
// is how it resumes the right place in that bot's update stream after a restart.
// A cursor that came back as zero would replay a conversation.
func TestHumanTaskTelegramRoundTrip(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	repo := st.HumanTasks()

	task := &models.HumanTask{
		ID:      "ht_1",
		Title:   "Confirm the address",
		Channel: models.HumanTaskTelegram,
		Mode:    models.HumanTaskPark,
		Telegram: &models.TelegramBinding{
			Connection: "conn_1",
			Alias:      "support-bot",
			ChatID:     "123456789",
			Cursor:     4711,
			Opened:     true,
		},
	}
	if err := repo.Upsert(ctx, task); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.GetByID(ctx, "ht_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Telegram == nil {
		t.Fatal("the Telegram binding did not survive the round trip")
	}
	if *got.Telegram != *task.Telegram {
		t.Fatalf("binding = %+v, want %+v", *got.Telegram, *task.Telegram)
	}

	// The bridge advances the cursor through the ordinary message/close paths, which
	// re-read and re-upsert the row — so an appended turn must not drop it.
	after, err := repo.AppendMessage(ctx, "ht_1", models.HumanTaskMessage{Role: "human", Text: "the Berlin office"})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
	if after.Telegram == nil || after.Telegram.Cursor != 4711 || !after.Telegram.Opened {
		t.Fatalf("binding after AppendMessage = %+v, want the cursor and opened flag kept", after.Telegram)
	}
}

// Every other channel stores no binding, and it has to read back as absent rather
// than as an empty one: the bridge treats a non-nil binding as a session to
// deliver, so an empty object would make it try to talk to chat "".
func TestHumanTaskWithoutTelegramBindingStaysNil(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	repo := st.HumanTasks()

	if err := repo.Upsert(ctx, &models.HumanTask{ID: "ht_2", Title: "Ask in the app"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.GetByID(ctx, "ht_2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Telegram != nil {
		t.Fatalf("Telegram = %+v, want nil for a direct task", got.Telegram)
	}
	// And the channel still defaults the way it always has.
	if got.Channel != models.HumanTaskDirect {
		t.Fatalf("channel = %q, want direct", got.Channel)
	}
}
