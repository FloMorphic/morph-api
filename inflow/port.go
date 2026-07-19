package inflow

import (
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/repository"
	fuse "github.com/Inflowenger/inflow-fusion/inflow"
	svcHandler "github.com/Inflowenger/inflow-fusion/svcHandler"
	"github.com/nats-io/nats.go"
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

func LoadSvcNodehandlers() error {
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
	return nil
}
