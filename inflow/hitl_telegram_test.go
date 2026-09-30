package inflow

import (
	"testing"

	"github.com/FloMorphic/morph-api/hitl"
	"github.com/FloMorphic/morph-api/models"
)

// tgTask is a live Telegram session bound to one chat, for the routing tests.
func tgTask(id, chat string, cursor int64, opened bool, updatedAt int64) *models.HumanTask {
	return &models.HumanTask{
		ID:        id,
		Channel:   models.HumanTaskTelegram,
		UpdatedAt: updatedAt,
		Telegram:  &models.TelegramBinding{ChatID: chat, Cursor: cursor, Opened: opened},
	}
}

func tgUpdate(id, chat int64, text string) hitl.TelegramUpdate {
	return hitl.TelegramUpdate{
		UpdateID: id,
		Message: &hitl.TelegramMessage{
			Text: text,
			Chat: hitl.TelegramChat{ID: chat},
		},
	}
}

// Every session bound to one bot reads the same update stream, so routing decides
// which conversation a reply belongs to. Getting it wrong writes one flow's
// answers into another flow's context, so each guard is worth pinning down.
func TestRouteUpdate(t *testing.T) {
	cases := []struct {
		name  string
		tasks []*models.HumanTask
		up    hitl.TelegramUpdate
		want  string
	}{
		{
			name:  "the session bound to that chat",
			tasks: []*models.HumanTask{tgTask("a", "111", 10, true, 1), tgTask("b", "222", 10, true, 1)},
			up:    tgUpdate(11, 222, "the Berlin office"),
			want:  "b",
		},
		{
			// The cursor is the acknowledgement point: a turn already answered must
			// not be answered again when a page is re-read for an older session.
			name:  "nothing for an update the session already consumed",
			tasks: []*models.HumanTask{tgTask("a", "111", 20, true, 1)},
			up:    tgUpdate(20, 111, "hello"),
			want:  "",
		},
		{
			// The bot has not spoken yet, so there is nothing for the person to be
			// replying to — whatever arrived belongs to the chat's past.
			name:  "nothing for a session that has not opened",
			tasks: []*models.HumanTask{tgTask("a", "111", 0, false, 1)},
			up:    tgUpdate(5, 111, "hello"),
			want:  "",
		},
		{
			name:  "nothing when no session is bound to that chat",
			tasks: []*models.HumanTask{tgTask("a", "111", 0, true, 1)},
			up:    tgUpdate(5, 999, "hello"),
			want:  "",
		},
		{
			// Two flows can legitimately be asking the same person about different
			// things; the newest question is the one being answered.
			name: "the most recently active session when two share a chat",
			tasks: []*models.HumanTask{
				tgTask("older", "111", 1, true, 1000),
				tgTask("newer", "111", 1, true, 2000),
			},
			up:   tgUpdate(9, 111, "yes"),
			want: "newer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routeUpdate(tc.tasks, tc.up)
			switch {
			case tc.want == "" && got != nil:
				t.Fatalf("routeUpdate() = %s, want no match", got.ID)
			case tc.want != "" && got == nil:
				t.Fatalf("routeUpdate() = no match, want %s", tc.want)
			case tc.want != "" && got.ID != tc.want:
				t.Fatalf("routeUpdate() = %s, want %s", got.ID, tc.want)
			}
		})
	}
}

// Updates are fetched per bot, not per task, so sessions must be bucketed by the
// bot they are bound to — one poll has to serve every session that bot holds, and
// a poll must never be issued against the wrong account.
func TestGroupByAccount(t *testing.T) {
	mk := func(id, connection, alias string) *models.HumanTask {
		return &models.HumanTask{ID: id, Telegram: &models.TelegramBinding{Connection: connection, Alias: alias, ChatID: "1"}}
	}
	groups := groupByAccount([]*models.HumanTask{
		mk("a", "conn1", "support"),
		mk("b", "conn1", "support"),
		mk("c", "conn1", "sales"),
		mk("d", "", ""),
	})
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3 (support, sales, default)", len(groups))
	}
	if len(groups[0].tasks) != 2 || groups[0].alias != "support" {
		t.Fatalf("first group = %+v, want the two support sessions", groups[0])
	}
	// A bot with no alias is the gateway's default account, and must be labelled as
	// something other than an empty string in the loop's log lines.
	if got := groups[2].label(); got != "default bot" {
		t.Fatalf("unaliased group label = %q, want %q", got, "default bot")
	}
}
