// Package svc holds the extrinsic svc-node handlers morph-api registers with the
// inflow runtime — Human-in-the-Loop, document store, and vector store — plus
// the embedding client the vector handler uses. Each handler's body is
// substantial (envelope parsing, store resolution, and for vectors an embedding
// round-trip), so keeping them here leaves inflow.LoadSvcNodehandlers a thin
// registration list rather than a file dominated by handler logic.
//
// Every handler has the shape func(store, nats.Header, []byte) ([]byte, error)
// so LoadSvcNodehandlers can register it directly.
package svc

import (
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
	"github.com/bytedance/sonic"
	"github.com/nats-io/nats.go"
	"strings"
)

// parseRequest unmarshals the extrinsic svc request envelope inflow-fusion
// delivers (v0.1.7+). A malformed body yields a zero envelope rather than an
// error so a handler can still record what it can.
func parseRequest(data []byte) inflowModels.ExtSvcRequestBody {
	var body inflowModels.ExtSvcRequestBody
	_ = sonic.Unmarshal(data, &body)
	return body
}

// decodeOp re-decodes the envelope's `op` map (the compile-time ExtrinsicRule.Data)
// into a typed payload T.
func decodeOp[T any](op map[string]any) T {
	var out T
	if len(op) == 0 {
		return out
	}
	if b, err := sonic.Marshal(op); err == nil {
		_ = sonic.Unmarshal(b, &out)
	}
	return out
}

// scopedDataMap returns the envelope's live scoped `Data` as an object, or nil
// when it is absent or not an object.
func scopedDataMap(data any) map[string]any {
	if m, ok := data.(map[string]any); ok && len(m) > 0 {
		return m
	}
	return nil
}

// firstNonEmpty returns the first argument that is not blank after trimming.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// actionFromSubject recovers the store action from the concrete subject the
// message arrived on (svc.store.<kind>.<action>), falling back to the body value.
func actionFromSubject(header nats.Header, fallback string) string {
	if subject := header.Get("recv_subject"); subject != "" {
		if parts := strings.Split(subject, "."); len(parts) > 0 {
			if last := parts[len(parts)-1]; last != "" && last != "*" {
				return last
			}
		}
	}
	return fallback
}
