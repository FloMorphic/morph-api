package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/sqlite/sqlcgen"
)

type extensionRepo struct {
	q *sqlcgen.Queries
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

	params, err := toParams(e)
	if err != nil {
		return err
	}
	return r.q.UpsertExtension(ctx, params)
}

func (r *extensionRepo) GetByID(ctx context.Context, id string) (*models.ExtensionRecord, error) {
	row, err := r.q.GetExtension(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return extensionFromRow(row)
}

func (r *extensionRepo) List(ctx context.Context, p repository.ListParams) ([]models.ExtensionRecord, int64, error) {
	limit := clampLimit(p.Limit)
	total, err := r.q.CountExtensions(ctx, sqlcgen.CountExtensionsParams{
		Search: p.Search,
		Kind:   p.Kind,
	})
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListExtensions(ctx, sqlcgen.ListExtensionsParams{
		Search: p.Search,
		Kind:   p.Kind,
		Offset: int64(p.Offset),
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.ExtensionRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := extensionFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *rec)
	}
	return items, total, nil
}

func (r *extensionRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteExtension(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// retiredBuiltinNames are the pre-morphic generic builtins that the current seed
// replaced. They are pruned on startup so an existing dev DB's palette matches
// the seeded set (idempotent — a missing row is a no-op).
var retiredBuiltinNames = []string{"PluginNative", "Code", "Contract", "Extrinsics", "Goto", "Void"}

// SeedBuiltins inserts each builtin whose name is not already present, keyed by
// name, so restarts don't duplicate and admin edits are never clobbered. It also
// prunes the retired generic builtins so a re-seeded DB reflects the current set.
func (r *extensionRepo) SeedBuiltins(ctx context.Context, defs []models.ExtensionRecord) (int, error) {
	// Prune retired builtins (idempotent).
	for _, name := range retiredBuiltinNames {
		row, err := r.q.GetBuiltinByName(ctx, name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return 0, err
		}
		if _, err := r.q.DeleteExtension(ctx, row.ID); err != nil {
			return 0, err
		}
	}

	inserted := 0
	for i := range defs {
		def := defs[i]
		if row, err := r.q.GetBuiltinByName(ctx, def.Name); err == nil {
			// Already seeded. Backfill the hard-coded pluginId onto the existing
			// row if the seed now carries one and the row is missing it (so the
			// plugin-backed builtins gain their credential wiring on re-seed
			// without clobbering any admin edits to the other fields).
			if def.PluginID != "" && row.PluginID == "" {
				existing, err := extensionFromRow(row)
				if err != nil {
					return inserted, err
				}
				existing.PluginID = def.PluginID
				if err := r.Upsert(ctx, existing); err != nil {
					return inserted, err
				}
			}
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return inserted, err
		}
		def.ID = ""       // force a fresh id
		def.Kind = models.KindBuiltin
		if err := r.Upsert(ctx, &def); err != nil {
			return inserted, err
		}
		inserted++
	}
	return inserted, nil
}

// toParams marshals an ExtensionRecord's JSON columns and assembles the upsert
// params. The record's ID/timestamps/kind must already be resolved.
func toParams(e *models.ExtensionRecord) (sqlcgen.UpsertExtensionParams, error) {
	icon, err := json.Marshal(e.Icon)
	if err != nil {
		return sqlcgen.UpsertExtensionParams{}, fmt.Errorf("sqlite: marshal extension icon: %w", err)
	}
	params, err := json.Marshal(e.Parameters)
	if err != nil {
		return sqlcgen.UpsertExtensionParams{}, fmt.Errorf("sqlite: marshal extension params: %w", err)
	}
	bindTo, err := json.Marshal(e.BindTo)
	if err != nil {
		return sqlcgen.UpsertExtensionParams{}, fmt.Errorf("sqlite: marshal extension bindTo: %w", err)
	}
	return sqlcgen.UpsertExtensionParams{
		ID:          e.ID,
		Kind:        string(e.Kind),
		Type:        string(e.Type),
		Name:        e.Name,
		Description: e.Description,
		PluginID:    e.PluginID,
		Icon:        string(icon),
		Params:      string(params),
		BindTo:      string(bindTo),
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
	}, nil
}

func extensionFromRow(row sqlcgen.Extension) (*models.ExtensionRecord, error) {
	rec := &models.ExtensionRecord{
		ID:          row.ID,
		Kind:        models.ExtensionKind(row.Kind),
		Type:        models.ExtensionType(row.Type),
		Name:        row.Name,
		Description: row.Description,
		PluginID:    row.PluginID,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.Icon != "" {
		if err := json.Unmarshal([]byte(row.Icon), &rec.Icon); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal extension icon for %s: %w", row.ID, err)
		}
	}
	if row.Params != "" {
		if err := json.Unmarshal([]byte(row.Params), &rec.Parameters); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal extension params for %s: %w", row.ID, err)
		}
	}
	if row.BindTo != "" {
		if err := json.Unmarshal([]byte(row.BindTo), &rec.BindTo); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal extension bindTo for %s: %w", row.ID, err)
		}
	}
	return rec, nil
}
