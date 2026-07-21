package inflow

import (
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/inflow/svc"
	"github.com/FloMorphic/morph-api/repository"
	fuse "github.com/Inflowenger/inflow-fusion/inflow"
	svcHandler "github.com/Inflowenger/inflow-fusion/svcHandler"
	"github.com/nats-io/nats.go"
)

// SvcHitl is the logical service name of the Human-in-the-Loop handler, and
// HitlSubject the subject it answers on. The nodeId travels in the request body
// (see compiler.go's hitl case) so the handler can recover the node.
const (
	SvcHitl     = "hitl"
	HitlSubject = "svc.hitl.add"

	// Store + scheduling service names/subjects. inflo-fusion owns the real
	// document/vector read-write; morph-api registers ack-only stubs so flows
	// that use these nodes don't hang when the store service is absent.
	SvcStoreDoc     = "store_doc"
	StoreDocSubject = "svc.store.doc.*"
	SvcStoreVec     = "store_vec"
	StoreVecSubject = "svc.store.vec.*"
	SvcContinueAt   = "continue_at"
	ContinueSubj    = "svc.continue.at"
)

// InitInflowConnection connects to the inflow runtime, wiring the backend
// contract to the given repository store.
func InitInflowConnection(store repository.Store) error {
	return fuse.InitBackend(
		fuse.WithImplementedBackendBy(&InflowWire{store: store}),
		fuse.WithJwtSecretKey(env.GetInfraJWTSecret()), // env INFLOW_INFRA_JWT_SECRET
		fuse.WithInfraApi(env.GetInfraApiUrl()),        // env INFLOW_INFRA_API
	)

}

// LoadSvcNodehandlers registers this backend's extrinsic svc handlers. They need
// the store so a running workflow's request can be persisted. The handler bodies
// live in the inflow/svc package (HITL, doc store, vector store + embedding) so
// this stays a readable registration list.
func LoadSvcNodehandlers(store repository.Store) error {
	svc_sub1 := "svc.add.issue.{TABLE_NAME}"
	err := svcHandler.ImplHandlerOnSubject("exports_db", svcHandler.SvcTopic(svc_sub1), func(header nats.Header, data []byte) ([]byte, error) {
		subject := header.Get("recv_subject")
		fmt.Printf("recieved Message On Subject %s with data %s\n", subject, string(data))
		table := strings.Split(subject, ".")[3]
		return []byte(fmt.Sprintf(`{"status":"saved successfully on %s table"}`, table)), nil
	})
	if err != nil {
		return fmt.Errorf("failed to create service node : %v", err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(svc_sub1).ConvertToSubscribe())

	// Human-in-the-Loop: when a workflow reaches a `humanInLoop` node the engine
	// publishes here. svc.HandleHumanTask records the task (pid / flowId / nodeId /
	// contextId + questions), captures the node's outbound edges, and replies with
	// a `stop` command so the runtime parks the flow until a resume run restarts
	// from the captured nexts.
	if err := svcHandler.ImplHandlerOnSubject(SvcHitl, svcHandler.SvcTopic(HitlSubject), func(header nats.Header, data []byte) ([]byte, error) {
		return svc.HandleHumanTask(store, header, data)
	}); err != nil {
		return fmt.Errorf("failed to create hitl service node : %v", err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(HitlSubject).ConvertToSubscribe())

	// Document store service. A `read` runs a validated, read-only SQL query
	// against the store's database; a `write` stores the request's JSON object
	// as a document. The concrete action is the last subject token
	// (svc.store.doc.{read,write}).
	if err := svcHandler.ImplHandlerOnSubject(SvcStoreDoc, svcHandler.SvcTopic(StoreDocSubject), func(header nats.Header, data []byte) ([]byte, error) {
		return svc.HandleDocStore(store, header, data)
	}); err != nil {
		return fmt.Errorf("failed to create %s service node : %v", SvcStoreDoc, err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(StoreDocSubject).ConvertToSubscribe())

	// Vector store service. A `write`/`index` embeds the request's text (using
	// the store's captured provider/model/token) and stores the vector; a
	// `read`/`search` embeds the query text and runs a KNN similarity search.
	// Both reuse the embedding config captured once when the store was created,
	// so a running flow never re-supplies a provider or vector size.
	if err := svcHandler.ImplHandlerOnSubject(SvcStoreVec, svcHandler.SvcTopic(StoreVecSubject), func(header nats.Header, data []byte) ([]byte, error) {
		return svc.HandleVecStore(store, header, data)
	}); err != nil {
		return fmt.Errorf("failed to create %s service node : %v", SvcStoreVec, err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(StoreVecSubject).ConvertToSubscribe())

	// Continue-After: park-and-resume at a scheduled time. HandleContinueAfter
	// records a scheduled process (StartWorkflow with ScheduledAt) whose start is
	// the captured `nextNodes`, tagged with its origin flow/node, and stops the
	// live run so it parks here.
	if err := svcHandler.ImplHandlerOnSubject(SvcContinueAt, svcHandler.SvcTopic(ContinueSubj), func(header nats.Header, data []byte) ([]byte, error) {
		return HandleContinueAfter(store, header, data)
	}); err != nil {
		return fmt.Errorf("failed to create continue service node : %v", err)
	}
	fmt.Println("New SVC handler registered On  ", svcHandler.SvcTopic(ContinueSubj).ConvertToSubscribe())

	// Subscribe to the engine's event log so finished runs close their process
	// rows. Non-fatal: the CRUD + launch paths keep working without it.
	if err := SubscribeProcessEvents(store); err != nil {
		fmt.Printf("warning: process events not subscribed: %v\n", err)
	}
	return nil
}
