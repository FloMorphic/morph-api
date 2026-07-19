package inflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
	"github.com/bytedance/sonic"
	"github.com/nats-io/nats.go"
)

// InflowWire implements the inflow-fusion backend contract. It bridges runtime
// requests from the workflow engine (fetch a context, persist a context update,
// fetch a compiled flow) to the repository store.
type InflowWire struct {
	store repository.Store
}

// RetrieveContext answers `inflow.req.context.get.{ctxId}` with the stored
// context document, or `{}` when it is missing.
func (w *InflowWire) RetrieveContext(msg *nats.Msg) {
	parts := strings.Split(msg.Subject, ".")
	ctxId := parts[len(parts)-1]

	rec, err := w.store.Contexts().GetByID(context.Background(), ctxId)
	if err != nil {
		msg.Respond([]byte(`{}`))
		return
	}
	doc := inflowModels.ContextDoc{Header: rec.Header, Data: rec.Context}
	b, _ := sonic.Marshal(doc)
	msg.Respond(b)
}

// UpdateContext persists a context mutation emitted by a running flow.
func (w *InflowWire) UpdateContext(msg *nats.Msg) {
	contextId := msg.Header.Get("contextId")

	var incoming inflowModels.ContextDoc
	if err := sonic.Unmarshal(msg.Data, &incoming); err != nil {
		fmt.Printf("inflow: invalid context payload for %s: %v\n", contextId, err)
		return
	}
	if repository.HasPrefix(contextId, repository.ContextIDPrefix) {
		rec, err := w.store.Contexts().GetByID(context.Background(), contextId)
		if err != nil {
			fmt.Printf("inflow: context %s not found\n", contextId)
			return
		}
		rec.Context = incoming.Data
		rec.Header = incoming.Header
		rec.UpdatedBy = models.LastChange{By: models.ByFlow, Address: msg.Header.Get("flowId")}
		if err := w.store.Contexts().Upsert(context.Background(), rec); err != nil {
			fmt.Printf("inflow: failed to update context %s: %v\n", contextId, err)
			return
		}
	}
	msg.Respond([]byte(`accepted`))
}

// RetrieveFlow answers `inflow.req.flow.get.{flowId}` with the compiled flow.
func (w *InflowWire) RetrieveFlow(msg *nats.Msg) {
	parts := strings.Split(msg.Subject, ".")
	flowId := parts[len(parts)-1]

	rec, err := w.store.Workflows().GetByID(context.Background(), flowId)
	if err != nil {
		msg.Respond([]byte(`{}`))
		fmt.Println("inflow: flow not found:", flowId)
		return
	}
	_, compiled, err := FLowCompiler(*rec)
	if err != nil {
		msg.Respond([]byte(`{}`))
		fmt.Printf("inflow: compile flow %s: %v\n", flowId, err)
		return
	}
	flow := inflowModels.Flow{UUID: flowId, Nodes: []inflowModels.Node{}}
	for _, node := range compiled {
		flow.Nodes = append(flow.Nodes, *node)
	}
	b, _ := sonic.Marshal(flow)
	msg.Respond(b)
}
