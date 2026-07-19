package models

// MemoryType distinguishes the two shapes of a memory store.
type MemoryType string

const (
	MemoryVector   MemoryType = "vector"
	MemoryDocument MemoryType = "document"
)

// VectorMetric is the distance function used by the vector index.
type VectorMetric string

const (
	MetricCosine    VectorMetric = "cosine"
	MetricDot       VectorMetric = "dot"
	MetricEuclidean VectorMetric = "euclidean"
)

// ColumnType is a document-store column's logical type.
type ColumnType string

// VectorMemoryConfig configures a vector store: an embedding model to turn text
// into vectors plus the index parameters. When a vector store is created the
// sqlite repository provisions a companion sqlite-vec (vec0) virtual table of
// `Dimensions` width so similarity search is available.
type VectorMemoryConfig struct {
	Provider       string       `json:"provider"`
	EmbeddingModel string       `json:"embeddingModel"`
	Token          string       `json:"token"`
	Dimensions     int          `json:"dimensions"`
	Metric         VectorMetric `json:"metric"`
	Namespace      string       `json:"namespace"`
}

// TableColumn describes one column of a document store's schema.
type TableColumn struct {
	Name    string     `json:"name"`
	Type    ColumnType `json:"type"`
	Primary bool       `json:"primary,omitempty"`
}

// DocumentMemoryConfig configures a document store: a table name and its schema.
type DocumentMemoryConfig struct {
	Table   string        `json:"table"`
	Columns []TableColumn `json:"columns"`
}

// MemoryStore is a named, reusable store referenced by Memory nodes in a
// workflow. Exactly one of Vector/Document is populated, per Type. JSON tags
// mirror flomorphic-wapp's MemoryStore interface.
type MemoryStore struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Type        MemoryType            `json:"type"`
	Description string                `json:"description"`
	Vector      *VectorMemoryConfig   `json:"vector,omitempty"`
	Document    *DocumentMemoryConfig `json:"document,omitempty"`
	CreatedAt   int64                 `json:"createdAt"`
	UpdatedAt   int64                 `json:"updatedAt"`
}
