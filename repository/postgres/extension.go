package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/seed"
)

const (
	extensionColumns = `id, kind, type, name, description, plugin_id, icon, params, bind_to,
	install, action, parent_id, outbound, created_at, updated_at`

	extensionUpsertSQL = `
INSERT INTO extensions (
    id, kind, type, name, description, plugin_id, icon, params, bind_to,
    install, action, parent_id, outbound, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (id) DO UPDATE SET
    kind = EXCLUDED.kind,
    type = EXCLUDED.type,
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    plugin_id = EXCLUDED.plugin_id,
    icon = EXCLUDED.icon,
    params = EXCLUDED.params,
    bind_to = EXCLUDED.bind_to,
    install = EXCLUDED.install,
    action = EXCLUDED.action,
    parent_id = EXCLUDED.parent_id,
    outbound = EXCLUDED.outbound,
    updated_at = EXCLUDED.updated_at`

	// $1 search, $2 kind — the admin panel lists builtins, the portal extensions.
	extensionFilterSQL = `($1::text = '' OR name ILIKE '%' || $1::text || '%')
	  AND ($2::text = '' OR kind = $2::text)`
)

type extensionRepo struct {
	db *sql.DB
}

func (r *extensionRepo) Upsert(ctx context.Context, e *models.ExtensionRecord) error {
	now := nowMillis()
	if e.ID == "" {
		e.ID = repository.NewID(repository.ExtensionIDPrefix)
		if e.CreatedAt == 0 {
			e.CreatedAt = now
		}
	} else if existing, err := r.GetByID(ctx, e.ID); err == nil {
		e.CreatedAt = existing.CreatedAt
	} else if errors.Is(err, repository.ErrNotFound) {
		if e.CreatedAt == 0 {
			e.CreatedAt = now
		}
	} else {
		return err
	}
	e.UpdatedAt = now
	if e.Kind == "" {
		e.Kind = models.KindExtension
	}

	icon, err := json.Marshal(e.Icon)
	if err != nil {
		return fmt.Errorf("postgres: marshal extension icon: %w", err)
	}
	params, err := json.Marshal(e.Parameters)
	if err != nil {
		return fmt.Errorf("postgres: marshal extension params: %w", err)
	}
	bindTo, err := json.Marshal(e.BindTo)
	if err != nil {
		return fmt.Errorf("postgres: marshal extension bindTo: %w", err)
	}
	install, err := json.Marshal(e.Install)
	if err != nil {
		return fmt.Errorf("postgres: marshal extension install: %w", err)
	}
	outbound, err := json.Marshal(e.Outbound)
	if err != nil {
		return fmt.Errorf("postgres: marshal extension outbound: %w", err)
	}

	_, err = r.db.ExecContext(ctx, extensionUpsertSQL,
		e.ID, string(e.Kind), string(e.Type), e.Name, e.Description, e.PluginID,
		string(icon), string(params), string(bindTo), string(install),
		e.Action, e.ParentID, string(outbound), e.CreatedAt, e.UpdatedAt)
	return err
}

func (r *extensionRepo) GetByID(ctx context.Context, id string) (*models.ExtensionRecord, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+extensionColumns+` FROM extensions WHERE id = $1`, id)
	return scanExtension(row)
}

func (r *extensionRepo) List(ctx context.Context, p repository.ListParams) ([]models.ExtensionRecord, int64, error) {
	limit := clampLimit(p.Limit)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM extensions WHERE `+extensionFilterSQL,
		p.Search, p.Kind).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+extensionColumns+` FROM extensions WHERE `+extensionFilterSQL+`
		 ORDER BY updated_at DESC, id DESC LIMIT $3 OFFSET $4`,
		p.Search, p.Kind, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items, err := collectExtensions(rows, limit)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *extensionRepo) Delete(ctx context.Context, id string) error {
	return execExpectRow(ctx, r.db, `DELETE FROM extensions WHERE id = $1`, id)
}

// ListPluginActions returns the palette rows synced from one plugin's actions,
// oldest first, so a sync that has to pick between two rows claiming the same
// identity keeps the one the workflows have had longest. Zero rows is not an
// error — a plugin that has never been synced simply has none.
func (r *extensionRepo) ListPluginActions(ctx context.Context, pluginID string) ([]models.ExtensionRecord, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+extensionColumns+` FROM extensions
		 WHERE plugin_id = $1 AND action <> ''
		 ORDER BY created_at ASC, id ASC`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectExtensions(rows, 16)
}

// DeletePluginActions drops the palette rows derived from one plugin's actions,
// keeping the plugin's own registration row (action = ”). Zero deleted is not
// an error — a plugin that has never been synced simply has none.
func (r *extensionRepo) DeletePluginActions(ctx context.Context, pluginID string) (int, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM extensions WHERE plugin_id = $1 AND action <> ''`, pluginID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// SeedBuiltins inserts each builtin whose name is not already present, keyed by
// name, so restarts don't duplicate and admin edits are never clobbered. It also
// prunes the retired generic builtins so a re-seeded database reflects the
// current set.
func (r *extensionRepo) SeedBuiltins(ctx context.Context, defs []models.ExtensionRecord) (int, error) {
	// Prune retired builtins (idempotent).
	for _, name := range seed.RetiredBuiltinNames {
		row, err := r.getBuiltinByName(ctx, name)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		} else if err != nil {
			return 0, err
		}
		if err := r.Delete(ctx, row.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
			return 0, err
		}
	}

	inserted := 0
	for i := range defs {
		def := defs[i]
		if existing, err := r.getBuiltinByName(ctx, def.Name); err == nil {
			// Already seeded. Backfill the hard-coded pluginId onto the existing
			// row if the seed now carries one and the row is missing it (so the
			// plugin-backed builtins gain their credential wiring on re-seed
			// without clobbering any admin edits to the other fields).
			if def.PluginID != "" && existing.PluginID == "" {
				existing.PluginID = def.PluginID
				if err := r.Upsert(ctx, existing); err != nil {
					return inserted, err
				}
			}
			continue
		} else if !errors.Is(err, repository.ErrNotFound) {
			return inserted, err
		}
		def.ID = "" // force a fresh id
		def.Kind = models.KindBuiltin
		if err := r.Upsert(ctx, &def); err != nil {
			return inserted, err
		}
		inserted++
	}
	return inserted, nil
}

// getBuiltinByName is the identity seeding matches on: a builtin is recognised
// by its name, not by an id the seed file would have to pin.
func (r *extensionRepo) getBuiltinByName(ctx context.Context, name string) (*models.ExtensionRecord, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+extensionColumns+` FROM extensions WHERE kind = 'builtin' AND name = $1`, name)
	return scanExtension(row)
}

func collectExtensions(rows *sql.Rows, capacity int) ([]models.ExtensionRecord, error) {
	items := make([]models.ExtensionRecord, 0, capacity)
	for rows.Next() {
		rec, err := scanExtension(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func scanExtension(row scanner) (*models.ExtensionRecord, error) {
	var (
		rec                                     models.ExtensionRecord
		kind, typ                               string
		icon, params, bindTo, install, outbound string
	)
	if err := row.Scan(&rec.ID, &kind, &typ, &rec.Name, &rec.Description, &rec.PluginID,
		&icon, &params, &bindTo, &install, &rec.Action, &rec.ParentID, &outbound,
		&rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	rec.Kind = models.ExtensionKind(kind)
	rec.Type = models.ExtensionType(typ)

	for _, f := range []struct {
		name string
		raw  string
		dst  any
	}{
		{"icon", icon, &rec.Icon},
		{"params", params, &rec.Parameters},
		{"bindTo", bindTo, &rec.BindTo},
		{"install", install, &rec.Install},
		{"outbound", outbound, &rec.Outbound},
	} {
		if f.raw == "" {
			continue
		}
		if err := json.Unmarshal([]byte(f.raw), f.dst); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal extension %s for %s: %w", f.name, rec.ID, err)
		}
	}
	return &rec, nil
}
