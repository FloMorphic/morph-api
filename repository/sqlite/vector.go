package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// maxVectorSearchK caps how many neighbours a single similarity search may
// return, so a request can never ask the index for an unbounded scan.
const maxVectorSearchK = 100

// vecPartitionColumn is the vec0 metadata column that holds each record's
// partition/tag key. It is a real (filterable) metadata column — not a `+`
// auxiliary one — because sqlite-vec only lets a KNN query constrain metadata
// and partition columns, so filtered top-k search needs it declared here.
const vecPartitionColumn = "partition"

// VectorIndex is the sqlite-vec similarity index behind the vector memory
// stores: one vec0 virtual table per store, provisioned on demand.
//
// It is a standalone engine rather than part of the sqlite repository because
// sqlite-vec is the only vector index FloMorphic ships, and a deployment that
// moves its records to postgres keeps using it: the postgres backend holds the
// store rows and the document tables, and points its vector operations at a
// sqlite file through this type. Nothing here reads the `memory_stores` table —
// a store's id and its captured VectorMemoryConfig are all an index needs — so
// the same index serves either backend's bookkeeping.
type VectorIndex struct {
	db *sql.DB
	// owned is true when this index opened its own connection (the sidecar case)
	// and so must close it. An index sharing the sqlite store's connection must
	// not close it out from under the repositories.
	owned bool
}

// OpenVectorIndex opens a sqlite database purely as a vector index — the sidecar
// a non-sqlite backend keeps its embeddings in. It applies no application
// schema: the file holds only the per-store vec0 tables, so pointing this at the
// deployment's existing sqlite file leaves every vector store exactly where it
// already was.
func OpenVectorIndex(source string) (*VectorIndex, error) {
	if err := ensureDBDir(source); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", buildDSN(source))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open vector index: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: ping vector index: %w", err)
	}
	x := &VectorIndex{db: db, owned: true}
	if _, err := x.Version(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return x, nil
}

// newVectorIndex wraps the sqlite store's own connection, so a sqlite
// deployment keeps its vectors in the same file as everything else.
func newVectorIndex(db *sql.DB) *VectorIndex { return &VectorIndex{db: db} }

// Close releases the index's connection when it owns one.
func (x *VectorIndex) Close() error {
	if x == nil || !x.owned {
		return nil
	}
	return x.db.Close()
}

// Version reports the loaded sqlite-vec version, and so doubles as the check
// that this database can host an index at all.
func (x *VectorIndex) Version(ctx context.Context) (string, error) {
	var v string
	if err := x.db.QueryRowContext(ctx, "SELECT vec_version()").Scan(&v); err != nil {
		return "", fmt.Errorf("sqlite: sqlite-vec not loaded: %w", err)
	}
	return v, nil
}

// Provision creates the vec0 virtual table that stores one store's embeddings.
// Table DDL cannot be parameterized, so the name is sanitized and the dimension
// is a validated integer; the distance metric comes from a fixed keyword set
// (never raw config text). Alongside the vector it keeps a filterable
// `partition` metadata column (a per-record namespace/tag) plus three auxiliary
// (unindexed) columns — the document id, its source text, and a JSON metadata
// blob — so a search can filter by partition and return the original content,
// not just a rowid.
func (x *VectorIndex) Provision(ctx context.Context, storeID string, cfg *models.VectorMemoryConfig) error {
	if cfg == nil || cfg.Dimensions <= 0 {
		return fmt.Errorf("sqlite: vector store %q needs a positive dimension count", storeID)
	}
	stmt := fmt.Sprintf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0("+
			"%s text, "+
			"embedding float[%d] distance_metric=%s, "+
			"+doc_id text, +content text, +metadata text)",
		vecTableName(storeID), vecPartitionColumn, cfg.Dimensions, cfg.NormalizedMetric(),
	)
	if _, err := x.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("sqlite: create vector index: %w", err)
	}
	return nil
}

// Drop removes one store's index.
func (x *VectorIndex) Drop(ctx context.Context, storeID string) error {
	_, err := x.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+vecTableName(storeID))
	return err
}

// Index stores one embedded vector plus its source text and metadata in the
// store's vec0 index. The vector's width is checked against the store's
// configured dimensions so a mismatched embedding is rejected before it reaches
// sqlite-vec (which would otherwise error opaquely). The document id, text, and
// serialized metadata are all bound as parameters — only the (sanitized) table
// name is interpolated.
func (x *VectorIndex) Index(ctx context.Context, store *models.MemoryStore, content string, vector []float32, metadata map[string]any, partition string) (string, error) {
	if store == nil || store.Type != models.MemoryVector || store.Vector == nil {
		return "", fmt.Errorf("sqlite: index requires a vector store")
	}
	if store.Vector.Dimensions > 0 && len(vector) != store.Vector.Dimensions {
		return "", fmt.Errorf("sqlite: vector has %d dimensions but store %q expects %d", len(vector), store.ID, store.Vector.Dimensions)
	}
	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return "", fmt.Errorf("sqlite: serialize vector: %w", err)
	}
	metaJSON := "{}"
	if len(metadata) > 0 {
		b, err := json.Marshal(metadata)
		if err != nil {
			return "", fmt.Errorf("sqlite: marshal vector metadata: %w", err)
		}
		metaJSON = string(b)
	}
	docID := repository.NewID(repository.VectorIDPrefix)
	table := vecTableName(store.ID)
	// Only write the partition column when this store's table actually has it —
	// tables created before partitioning stay writable in un-partitioned mode.
	if x.hasColumn(ctx, table, vecPartitionColumn) {
		stmt := fmt.Sprintf("INSERT INTO %s (%s, embedding, doc_id, content, metadata) VALUES (?, ?, ?, ?, ?)", table, vecPartitionColumn)
		if _, err := x.db.ExecContext(ctx, stmt, strings.TrimSpace(partition), blob, docID, content, metaJSON); err != nil {
			return "", fmt.Errorf("sqlite: index vector: %w", err)
		}
		return docID, nil
	}
	stmt := fmt.Sprintf("INSERT INTO %s (embedding, doc_id, content, metadata) VALUES (?, ?, ?, ?)", table)
	if _, err := x.db.ExecContext(ctx, stmt, blob, docID, content, metaJSON); err != nil {
		return "", fmt.Errorf("sqlite: index vector: %w", err)
	}
	return docID, nil
}

// DeleteRecord removes one indexed record from the store's vec0 index by its
// document id. doc_id is an unindexed auxiliary column, so vec0 will not let a
// WHERE clause constrain on it directly — the rowid is resolved with a scan
// first, then the delete runs by rowid. Returns repository.ErrNotFound when no
// record carries the id.
func (x *VectorIndex) DeleteRecord(ctx context.Context, store *models.MemoryStore, docID string) error {
	if store == nil || store.Type != models.MemoryVector {
		return fmt.Errorf("sqlite: delete requires a vector store")
	}
	docID = strings.TrimSpace(docID)
	if docID == "" {
		return fmt.Errorf("sqlite: delete requires a docId")
	}
	table := vecTableName(store.ID)

	rows, err := x.db.QueryContext(ctx, fmt.Sprintf("SELECT rowid, doc_id FROM %s", table))
	if err != nil {
		return fmt.Errorf("sqlite: lookup vector: %w", err)
	}
	var (
		rowID int64
		found bool
	)
	for rows.Next() {
		var (
			rid int64
			did string
		)
		if err := rows.Scan(&rid, &did); err != nil {
			rows.Close()
			return fmt.Errorf("sqlite: scan vector row: %w", err)
		}
		if did == docID {
			rowID, found = rid, true
			break
		}
	}
	if cerr := rows.Close(); cerr != nil && err == nil {
		return fmt.Errorf("sqlite: iterate vectors: %w", cerr)
	}
	if !found {
		return repository.ErrNotFound
	}
	if _, err := x.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", table), rowID); err != nil {
		return fmt.Errorf("sqlite: delete vector: %w", err)
	}
	return nil
}

// Search runs a KNN query over the store's vec0 index for the query vector,
// returning the k nearest matches (nearest first) with their stored text and
// metadata. The query vector width is validated up front and k is clamped so a
// caller can never ask for an unbounded scan.
func (x *VectorIndex) Search(ctx context.Context, store *models.MemoryStore, vector []float32, k int, partition string, minScore float64, filter map[string]any) ([]models.VectorMatch, error) {
	if store == nil || store.Type != models.MemoryVector || store.Vector == nil {
		return nil, fmt.Errorf("sqlite: search requires a vector store")
	}
	if store.Vector.Dimensions > 0 && len(vector) != store.Vector.Dimensions {
		return nil, fmt.Errorf("sqlite: query vector has %d dimensions but store %q expects %d", len(vector), store.ID, store.Vector.Dimensions)
	}
	if k <= 0 {
		k = 5
	}
	if k > maxVectorSearchK {
		k = maxVectorSearchK
	}
	// A metadata filter is applied in Go after the KNN — sqlite-vec can only
	// constrain declared metadata/partition columns, and the metadata blob is an
	// auxiliary column. Over-fetch the neighbourhood so the filter still has k
	// hits to keep, then trim back to k below.
	fetchK := k
	if len(filter) > 0 {
		fetchK = maxVectorSearchK
	}
	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return nil, fmt.Errorf("sqlite: serialize query vector: %w", err)
	}
	table := vecTableName(store.ID)
	hasPartition := x.hasColumn(ctx, table, vecPartitionColumn)
	// vec0 KNN: constrain with `embedding MATCH ?` and order by the virtual
	// `distance` column; LIMIT caps the neighbourhood. When a partition filter is
	// asked for (and the table has the column) it is ANDed into the MATCH query so
	// the top-k is computed within that partition, not across the whole index.
	partition = strings.TrimSpace(partition)
	selectCol := "''"
	where := "embedding MATCH ?"
	args := []any{blob}
	if hasPartition {
		selectCol = vecPartitionColumn
		if partition != "" {
			where += fmt.Sprintf(" AND %s = ?", vecPartitionColumn)
			args = append(args, partition)
		}
	} else if partition != "" {
		// The store predates partitioning: it has no partitioned records, so a
		// partition-scoped search can only be empty.
		return []models.VectorMatch{}, nil
	}
	args = append(args, fetchK)
	stmt := fmt.Sprintf(
		"SELECT doc_id, %s, content, metadata, distance FROM %s WHERE %s ORDER BY distance LIMIT ?",
		selectCol, table, where,
	)
	rows, err := x.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: vector search: %w", err)
	}
	defer rows.Close()

	out := make([]models.VectorMatch, 0, k)
	for rows.Next() {
		var (
			docID, part, content, metaJSON string
			distance                       float64
		)
		if err := rows.Scan(&docID, &part, &content, &metaJSON, &distance); err != nil {
			return nil, fmt.Errorf("sqlite: scan vector match: %w", err)
		}
		score := store.Vector.SimilarityScore(distance)
		// Rows come back nearest-first (distance ascending ⇒ score descending), so
		// the first hit under the threshold means every later one is too — stop.
		if minScore > 0 && score < minScore {
			break
		}
		m := models.VectorMatch{DocID: docID, Partition: part, Content: content, Distance: distance, Score: score}
		if metaJSON != "" && metaJSON != "{}" {
			var meta map[string]any
			if err := json.Unmarshal([]byte(metaJSON), &meta); err == nil {
				m.Metadata = meta
			}
		}
		// Metadata filter: keep only records carrying every requested key/value.
		if !metadataMatches(m.Metadata, filter) {
			continue
		}
		out = append(out, m)
		// The over-fetch pulled up to maxVectorSearchK candidates; return no more
		// than the k the caller asked for once the filter has had its say.
		if len(out) >= k {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate vector matches: %w", err)
	}
	return out, nil
}

// hasColumn reports whether the (already sanitized) table has a column of the
// given name. It lets the vector index/search paths degrade gracefully on a
// store whose vec0 table predates the `partition` column (vec0 has no ALTER, so
// an old table cannot gain it): those stores keep working un-partitioned instead
// of erroring on a missing column.
func (x *VectorIndex) hasColumn(ctx context.Context, table, column string) bool {
	rows, err := x.db.QueryContext(ctx, "SELECT 1 FROM pragma_table_info(?) WHERE name = ? LIMIT 1", table, column)
	if err != nil {
		return false
	}
	defer rows.Close()
	return rows.Next()
}

// metadataMatches reports whether a record's stored metadata satisfies every
// key/value pair in filter (AND semantics). An empty filter matches everything.
// Comparison is tolerant of JSON round-trip type drift — a number stored as
// float64 still matches a filter value that arrived as a string form — so a
// caller need not know the exact stored type of a field.
func metadataMatches(meta map[string]any, filter map[string]any) bool {
	if len(filter) == 0 {
		return true
	}
	for k, want := range filter {
		got, ok := meta[k]
		if !ok || !metaValueEqual(got, want) {
			return false
		}
	}
	return true
}

// metaValueEqual compares two metadata values with the JSON type-drift tolerance
// metadataMatches needs: an exact match, or equal string forms (so 42 == "42").
func metaValueEqual(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// vecTableName derives a safe vec0 table name from a store id.
func vecTableName(id string) string {
	var b strings.Builder
	b.WriteString("vec_")
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
