package sqlite

import (
	"context"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/sqlite/sqlcgen"
)

type telegramRecipientRepo struct {
	q *sqlcgen.Queries
}

// Upsert records (or refreshes) one reachable chat. The id is derived from the
// bot scope plus the chat id, so seeing the same chat again updates its row rather
// than adding another — which is the whole point: the directory accumulates who a
// bot can reach, it is not a log of messages.
func (r *telegramRecipientRepo) Upsert(ctx context.Context, rec *models.TelegramRecipient) error {
	now := nowMillis()
	rec.Connection = strings.TrimSpace(rec.Connection)
	rec.Alias = strings.TrimSpace(rec.Alias)
	rec.ChatID = strings.TrimSpace(rec.ChatID)
	rec.ID = models.TelegramRecipientID(rec.Connection, rec.Alias, rec.ChatID)
	if rec.CreatedAt == 0 {
		rec.CreatedAt = now
	}
	if rec.LastSeenAt == 0 {
		rec.LastSeenAt = now
	}
	rec.UpdatedAt = now

	return r.q.UpsertTelegramRecipient(ctx, sqlcgen.UpsertTelegramRecipientParams{
		ID:         rec.ID,
		Connection: rec.Connection,
		Alias:      rec.Alias,
		ChatID:     rec.ChatID,
		Type:       rec.Type,
		Title:      rec.Title,
		Username:   rec.Username,
		FirstName:  rec.FirstName,
		LastName:   rec.LastName,
		LastSeenAt: rec.LastSeenAt,
		CreatedAt:  rec.CreatedAt,
		UpdatedAt:  rec.UpdatedAt,
	})
}

// List returns one bot's known recipients, most recently heard from first — the
// order a picker wants, and the order that answers "who is still reachable?".
func (r *telegramRecipientRepo) List(ctx context.Context, connection, alias string, limit int) ([]models.TelegramRecipient, error) {
	rows, err := r.q.ListTelegramRecipients(ctx, sqlcgen.ListTelegramRecipientsParams{
		Connection: strings.TrimSpace(connection),
		Alias:      strings.TrimSpace(alias),
		Limit:      int64(clampRecipientLimit(limit)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]models.TelegramRecipient, 0, len(rows))
	for _, row := range rows {
		out = append(out, models.TelegramRecipient{
			ID:         row.ID,
			Connection: row.Connection,
			Alias:      row.Alias,
			ChatID:     row.ChatID,
			Type:       row.Type,
			Title:      row.Title,
			Username:   row.Username,
			FirstName:  row.FirstName,
			LastName:   row.LastName,
			LastSeenAt: row.LastSeenAt,
			CreatedAt:  row.CreatedAt,
			UpdatedAt:  row.UpdatedAt,
		})
	}
	return out, nil
}

func (r *telegramRecipientRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteTelegramRecipient(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// clampRecipientLimit bounds a page. The directory is a picker's worth of rows,
// not a dataset; 200 is generous for one bot's contacts and keeps an unbounded
// query out of the HTTP path.
func clampRecipientLimit(limit int) int {
	if limit <= 0 {
		return 200
	}
	if limit > 500 {
		return 500
	}
	return limit
}
