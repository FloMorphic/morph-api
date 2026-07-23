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

// VectorMemoryConfig configures a vector store: the LLM provider + embedding
// model used to turn text into vectors, plus the index parameters. These are
// captured once, when the store is created (see the memory `create` flow), and
// the vector svc handler reuses them for every index/search — the user is never
// asked for a provider or vector size again. When a vector store is created the
// sqlite repository provisions a companion sqlite-vec (vec0) virtual table of
// `Dimensions` width so similarity search is available.
type VectorMemoryConfig struct {
	// Provider is the embedding LLM provider (e.g. "openai"), or a full base URL
	// of an OpenAI-compatible embeddings endpoint. EmbeddingModel is the model
	// name and Token the API key both used at index/search time. Dimensions is
	// the vector width the index is built for and every embedding is checked
	// against.
	Provider       string       `json:"provider"`
	EmbeddingModel string       `json:"embeddingModel"`
	Token          string       `json:"token"`
	Dimensions     int          `json:"dimensions"`
	Metric         VectorMetric `json:"metric"`
	Namespace      string       `json:"namespace"`
}

// SQLiteDistanceMetric maps the configured metric to the keyword sqlite-vec's
// vec0 accepts on a vector column (`distance_metric=`). Defaults to cosine,
// which is the sensible default for text embeddings; dot product has no direct
// vec0 equivalent, so it also falls back to cosine (unit-norm embeddings make
// the two rank identically).
func (c *VectorMemoryConfig) SQLiteDistanceMetric() string {
	switch c.Metric {
	case MetricEuclidean:
		return "L2"
	case MetricCosine, MetricDot, "":
		return "cosine"
	default:
		return "cosine"
	}
}

// VectorMatch is one hit from a vector similarity search: the stored document
// id, its original text/metadata, and the distance to the query vector (smaller
// is closer, per the store's metric).
type VectorMatch struct {
	DocID string `json:"docId"`
	// Partition is the record's partition/tag key, echoed back so a caller can
	// see which partition a hit came from. Empty for records stored without one.
	Partition string         `json:"partition,omitempty"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Distance  float64        `json:"distance"`
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
