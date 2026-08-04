package hitlControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/etc"
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
	})
	if err != nil {
		return etc.FailFromRepo(c, err, "human tasks not found")
	}
	return etc.OK(c, models.NewPage(items, total, q))
}

// getByID handles GET /hitl/id/:id — open a task as a conversation.
//
// This is where the session's prompt and context refs are rendered: the svc
// handler could not resolve them at run time, so opening the task is the first
// moment they can become text. See resolveSession.
func (ctl *controller) getByID(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	resolveSession(rec)
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
	resolveSession(rec)
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
	resolveSession(rec)
	return etc.OK(c, rec)
}

// close handles POST /hitl/id/:id/close — force-finish the task; the workflow
// terminates at this step.
func (ctl *controller) close(c fiber.Ctx) error {
	rec, err := ctl.repo.Close(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "human task not found")
	}
	resolveSession(rec)
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
