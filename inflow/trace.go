package inflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/env"
	"github.com/Inflowenger/inflow-fusion/etc"
	fuse "github.com/Inflowenger/inflow-fusion/inflow"
	"github.com/bytedance/sonic"
)

// InfraTrace is the last-known lifecycle record infra keeps for a process pid.
//
// A fractal instance forgets a pid the moment its run ends, so once a run has
// finished the engine answers /ps/stop with a Not Found — even for a row this
// API still shows as `running` because a lost proc.finish never closed it (the
// case that motivates this lookup). Infra, however, retains a pid's trace for a
// week, which is enough to tell "already ended" from "genuinely still running":
//
//   - FinishAt > 0            → the run ended; Data.Status/DurationMs say how.
//   - StartAt > 0, FinishAt 0 → dispatched but not finished yet.
type InfraTrace struct {
	ID       string `json:"id"`
	PID      string `json:"pid"`
	Resource string `json:"resource"`
	StartAt  int64  `json:"start_at"`
	FinishAt int64  `json:"finish_at"`
	Data     struct {
		// Status is the engine finish status ("completed"/"stopped"/"failed"),
		// the same vocabulary finishStatus maps from proc.finish.
		Status     string `json:"status"`
		DurationMs int64  `json:"durationMs"`
	} `json:"data"`
}

// infraTraceResponse is the infra API envelope ({data, error}).
type infraTraceResponse struct {
	Data  *InfraTrace `json:"data"`
	Error any         `json:"error"`
}

// Finished reports whether the trace records the run as ended — the signal that
// it is safe to close out a row that is still marked running here.
func (t *InfraTrace) Finished() bool { return t != nil && t.FinishAt > 0 }

// fetchInfraTrace asks the infra trace service for the last-known status of a
// pid (GET <INFLOW_INFRA_API>/inflow/trace/<pid>), authenticated with the same
// infra bearer the backend uses for its other infra calls.
//
// Three outcomes, kept distinct because the caller acts differently on each:
//
//   - (record, nil) — infra has a trace; inspect FinishAt to know ended vs live.
//   - (nil, nil)    — infra positively has no record (404, or 200 with null
//     data): the pid is unknown here too, so there is *no evidence* the run is
//     still alive.
//   - (nil, err)    — the lookup itself failed (misconfig, unreachable, a non-404
//     error status, a decode failure): we simply could not determine the status.
func fetchInfraTrace(ctx context.Context, pid string) (*InfraTrace, error) {
	base := strings.TrimRight(strings.TrimSpace(env.GetInfraApiUrl()), "/")
	if base == "" {
		return nil, fmt.Errorf("infra api url (INFLOW_INFRA_API) not configured")
	}
	backend := fuse.GetInflowBackend()
	if backend == nil {
		return nil, fmt.Errorf("inflow backend not initialised")
	}

	url := fmt.Sprintf("%s/inflow/trace/%s", base, pid)
	resp, err := etc.SendHttpGetRaw(ctx, map[string]string{"Authorization": backend.GetBearerToken()}, url, 5*time.Second)
	if err != nil {
		return nil, err
	}
	// A 404 is a definitive "infra has no such trace", not a failure — surface it
	// as (nil, nil) so the caller can treat it as "no evidence" rather than an
	// error it must not act on. Any other non-200 is a real lookup failure.
	if resp.Status() == 404 {
		return nil, nil
	}
	if resp.Status() != 200 {
		return nil, fmt.Errorf("infra trace %s: %s (%d)", pid, resp.Body(), resp.Status())
	}

	var out infraTraceResponse
	if err := sonic.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("decode infra trace %s: %w", pid, err)
	}
	// 200 with an empty envelope is the same "no record" signal as a 404.
	if out.Data == nil {
		return nil, nil
	}
	return out.Data, nil
}
