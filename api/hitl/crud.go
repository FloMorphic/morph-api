package hitlControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

// list handles GET /hitl — page-based listing with an optional status filter
// (open / answered / closed) and title search.
func (ctl *controller) list(c fiber.Ctx) error {
	var q models.PaginationParams
	if err := c.Bind().Query(&q); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid query parameters")
	}
	if err := q.Normalize(); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	status := strings.TrimSpace(c.Query("status"))
	if !validStatusFilter(status) {
		return etc.Fail(c, fiber.StatusBadRequest, "status must be open, answered or closed")
	}
	items, total, err := ctl.repo.List(c.Context(), repository.ListParams{
		Offset: q.Offset(),
		Limit:  q.PerPage,
		Search: q.Search,
		Status: status,
		// Optional: scope to one flow's tasks — the editor uses it to warn when a
		// flow has open human tasks in flight.
		FlowID: strings.TrimSpace(c.Query("flowId")),
	})
	if err != nil {
		return etc.FailFromRepo(c, err, "human tasks not found")
	}
	return etc.OK(c, models.NewPage(items, total, q))
}

// getByID handles GET /hitl/id/:id — open a task as a conversation.
func (ctl *controller) getByID(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	return etc.OK(c, rec)
}

type answerInput struct {
	QuestionID string `json:"questionId"`
	Answer     string `json:"answer"`
}

// answer handles POST /hitl/id/:id/answer — record the human's answer to one
// question. The task flips to `answered` once every question is answered.
func (ctl *controller) answer(c fiber.Ctx) error {
	var in answerInput
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid answer payload")
	}
	if strings.TrimSpace(in.QuestionID) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "questionId is required")
	}
	rec, err := ctl.repo.Answer(c.Context(), c.Params("id"), in.QuestionID, in.Answer)
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	return etc.OK(c, rec)
}

type messageInput struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// message handles POST /hitl/id/:id/message — append one turn to the task's chat
// thread. The LLM assistant reply is produced on the frontend; the backend only
// stores the thread, so any role (human / assistant / system) is accepted.
func (ctl *controller) message(c fiber.Ctx) error {
	var in messageInput
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid message payload")
	}
	if strings.TrimSpace(in.Text) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "text is required")
	}
	role := strings.TrimSpace(in.Role)
	if role == "" {
		role = "human"
	}
	rec, err := ctl.repo.AppendMessage(c.Context(), c.Params("id"), models.HumanTaskMessage{
		Role: role,
		Text: in.Text,
	})
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	return etc.OK(c, rec)
}

// close handles POST /hitl/id/:id/close — finish the session and, for a task
// whose node parked the flow, continue that flow from where it stopped.
//
// Closing is what ends the person's turn, so it is also what releases the run:
// inflow.ResumeHumanTask launches a fresh run entered on every next node the
// parked node captured. A task that never parked its flow (`continue` mode) or
// had no outbound edges simply closes, with no run to start.
//
// An already-closed task is not resumed again — closing is idempotent on the
// record, but launching a run is not.
func (ctl *controller) close(c fiber.Ctx) error {
	before, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	alreadyClosed := before.Status == models.HumanTaskClosed

	rec, err := ctl.repo.Close(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	if alreadyClosed {
		return etc.OK(c, rec)
	}
	// Bind the conversation's outcome into the run's context under the node's key
	// before anything resumes, so a downstream/resumed node reads the person's
	// answers as `{{$.<key>}}`. A context that cannot be merged is reported but
	// does not block the close or the resume.
	if err := inflow.WriteHumanTaskContext(c.Context(), ctl.store, rec); err != nil {
		return etc.Send(c, fiber.StatusBadGateway, rec, err.Error())
	}
	if _, err := inflow.ResumeHumanTask(c.Context(), ctl.store, rec); err != nil {
		// The session is closed either way — report the failed resume, but hand
		// back the task so the UI reflects the state that was actually reached.
		return etc.Send(c, fiber.StatusBadGateway, rec, err.Error())
	}
	return etc.OK(c, rec)
}

// deleteByID handles DELETE /hitl/id/:id.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.repo.Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id}, nil)
}

// validStatusFilter allows an empty filter (any) or one of the three states.
func validStatusFilter(s string) bool {
	switch models.HumanTaskStatus(s) {
	case "", models.HumanTaskOpen, models.HumanTaskAnswered, models.HumanTaskClosed:
		return true
	default:
		return false
	}
}
