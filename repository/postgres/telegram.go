package postgres

import (
	"context"
	"database/sql"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

const (
	telegramColumns = `id, connection, alias, chat_id, type, title, username, first_name,
	last_name, last_seen_at, created_at, updated_at`

	// GREATEST on last_seen_at is the postgres spelling of the sqlite MAX():
	// a chat re-seen out of order must not walk the timestamp backwards.
	telegramUpsertSQL = `
INSERT INTO telegram_recipients (
    id, connection, alias, chat_id, type, title, username, first_name, last_name,
    last_seen_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO UPDATE SET
    type = EXCLUDED.type,
    title = EXCLUDED.title,
    username = EXCLUDED.username,
    first_name = EXCLUDED.first_name,
    last_name = EXCLUDED.last_name,
    last_seen_at = GREATEST(telegram_recipients.last_seen_at, EXCLUDED.last_seen_at),
    updated_at = EXCLUDED.updated_at`
)

type telegramRecipientRepo struct {
	db *sql.DB
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

	_, err := r.db.ExecContext(ctx, telegramUpsertSQL,
		rec.ID, rec.Connection, rec.Alias, rec.ChatID, rec.Type, rec.Title, rec.Username,
		rec.FirstName, rec.LastName, rec.LastSeenAt, rec.CreatedAt, rec.UpdatedAt)
	return err
}

// List returns one bot's known recipients, most recently heard from first — the
// order a picker wants, and the order that answers "who is still reachable?".
func (r *telegramRecipientRepo) List(ctx context.Context, connection, alias string, limit int) ([]models.TelegramRecipient, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+telegramColumns+` FROM telegram_recipients
		 WHERE connection = $1 AND alias = $2
		 ORDER BY last_seen_at DESC, id DESC LIMIT $3`,
		strings.TrimSpace(connection), strings.TrimSpace(alias), clampRecipientLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.TelegramRecipient, 0, 16)
	for rows.Next() {
		var rec models.TelegramRecipient
		if err := rows.Scan(&rec.ID, &rec.Connection, &rec.Alias, &rec.ChatID, &rec.Type,
			&rec.Title, &rec.Username, &rec.FirstName, &rec.LastName, &rec.LastSeenAt,
			&rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *telegramRecipientRepo) Delete(ctx context.Context, id string) error {
	return execExpectRow(ctx, r.db, `DELETE FROM telegram_recipients WHERE id = $1`, id)
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

var _ repository.TelegramRecipientRepository = (*telegramRecipientRepo)(nil)
