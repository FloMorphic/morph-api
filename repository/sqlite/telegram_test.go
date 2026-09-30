package sqlite

import (
	"context"
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// The directory accumulates who a bot can reach — it is not a message log — so
// re-seeing a chat has to refresh its row rather than add another, and its scope
// has to keep two bots' contacts apart: a chat id only means something to the bot
// that saw it.
func TestTelegramRecipientDirectory(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	repo := st.TelegramRecipients()

	first := &models.TelegramRecipient{
		Connection: "conn_1", Alias: "support", ChatID: "111",
		Type: "private", FirstName: "Mina", LastSeenAt: 1000,
	}
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// The same chat, heard from again with a name it did not have before.
	again := &models.TelegramRecipient{
		Connection: "conn_1", Alias: "support", ChatID: "111",
		Type: "private", FirstName: "Mina", LastName: "K", Username: "mina", LastSeenAt: 3000,
	}
	if err := repo.Upsert(ctx, again); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("the same chat got two ids (%s, %s)", first.ID, again.ID)
	}

	// A different bot's contact must not show up in this one's list.
	if err := repo.Upsert(ctx, &models.TelegramRecipient{
		Connection: "conn_1", Alias: "sales", ChatID: "222", Type: "private", FirstName: "Other",
	}); err != nil {
		t.Fatalf("upsert other bot: %v", err)
	}
	// And an older chat on the same bot, to pin the ordering.
	if err := repo.Upsert(ctx, &models.TelegramRecipient{
		Connection: "conn_1", Alias: "support", ChatID: "333", Type: "group", Title: "Ops room", LastSeenAt: 2000,
	}); err != nil {
		t.Fatalf("upsert group: %v", err)
	}

	list, err := repo.List(ctx, "conn_1", "support", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d recipients, want 2 (the re-seen chat was not merged, or scope leaked): %+v", len(list), list)
	}
	// Most recently heard from first — the order a picker wants.
	if list[0].ChatID != "111" || list[1].ChatID != "333" {
		t.Fatalf("order = %s, %s; want 111 then 333", list[0].ChatID, list[1].ChatID)
	}
	// The refresh carried the newly learned fields.
	if list[0].Username != "mina" || list[0].LastName != "K" {
		t.Fatalf("re-seen chat did not pick up its new fields: %+v", list[0])
	}

	// A stale contact can be forgotten, or a picker fills with people who left.
	if err := repo.Delete(ctx, list[1].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if after, _ := repo.List(ctx, "conn_1", "support", 0); len(after) != 1 {
		t.Fatalf("after delete got %d, want 1", len(after))
	}
}

// A chat re-seen out of order (a page read behind the newest) must not walk the
// "last heard from" timestamp backwards, or the picker's ordering degrades as the
// directory is written to.
func TestTelegramRecipientLastSeenNeverGoesBackwards(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	repo := st.TelegramRecipients()

	rec := &models.TelegramRecipient{Connection: "", Alias: "", ChatID: "9", LastSeenAt: 5000}
	if err := repo.Upsert(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rec.LastSeenAt = 1000
	if err := repo.Upsert(ctx, rec); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	list, _ := repo.List(ctx, "", "", 0)
	if len(list) != 1 || list[0].LastSeenAt != 5000 {
		t.Fatalf("lastSeenAt = %+v, want it held at 5000", list)
	}
}

// Naming is per chat kind: Telegram fills Title for a room, First/LastName for a
// person, Username when public — and a picker still needs something to show when it
// fills none of them.
func TestTelegramRecipientLabel(t *testing.T) {
	cases := []struct {
		rec  models.TelegramRecipient
		want string
	}{
		{models.TelegramRecipient{Title: "Ops room", ChatID: "1"}, "Ops room"},
		{models.TelegramRecipient{FirstName: "Mina", LastName: "K", ChatID: "1"}, "Mina K"},
		{models.TelegramRecipient{FirstName: "Mina", ChatID: "1"}, "Mina"},
		{models.TelegramRecipient{Username: "mina", ChatID: "1"}, "@mina"},
		{models.TelegramRecipient{ChatID: "123456789"}, "123456789"},
	}
	for _, tc := range cases {
		if got := tc.rec.Label(); got != tc.want {
			t.Fatalf("Label(%+v) = %q, want %q", tc.rec, got, tc.want)
		}
	}
}
