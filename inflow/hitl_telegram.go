package inflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/hitl"
	"github.com/FloMorphic/morph-api/llm"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

// The Telegram bridge: the Human-in-the-Loop facilitator, held in a Telegram
// chat instead of the app.
//
// A `direct` session is driven by the browser — the panel opens, calls
// /hitl/id/:id/start, and each reply is an HTTP round trip. Nothing about that
// works for a messenger: the person is not in the app, and their reply arrives at
// Telegram, not at us. So a `telegram` task needs something on the server that
// (a) opens the session by delivering the facilitator's first turn to the chat,
// and (b) watches the bot's update stream for the person's answers and keeps the
// conversation going. That is this loop.
//
// It is the same conversation as the in-app one, by construction: the prompt, the
// brief and the thread come from the shared session core (package hitl), and
// every turn — theirs and the bot's — is appended to the task, so Operate → Human
// Tasks remains the record of what was said and closing from the app still works.
//
// Why polling rather than a webhook: FloMorphic is deployed on-prem, so Telegram
// (and the OpenConnector gateway) generally cannot reach it. Polling is the one
// direction that always works from behind a firewall. It is an ordinary short
// poll — the loop owns the cadence, and `offset` acknowledges consumed updates so
// nothing is delivered twice or lost across a restart.
//
// One caveat worth knowing: a bot has a SINGLE update stream. If the same bot is
// also polled elsewhere (the telegram-oc plugin's Get updates action, or a
// webhook), the two consumers steal each other's updates — that is Telegram's
// design, not ours. Give a HITL bot to the bridge alone.
const (
	// hitlTelegramFastest is the poll interval while a conversation is actually
	// moving. Two seconds reads as immediate in a chat.
	hitlTelegramFastest = 2 * time.Second
	// hitlTelegramSlowest is where the ladder below bottoms out: a session that has
	// been silent for half an hour. The person has plainly gone elsewhere, so half a
	// minute to notice their reply is imperceptible — and it is the rate that decides
	// what a session left open overnight costs.
	hitlTelegramSlowest = 30 * time.Second
	// hitlTelegramIdle is the sleep when no Telegram session is open. Nothing is
	// polled then — the loop only re-checks the task table.
	hitlTelegramIdle = 20 * time.Second
	// hitlTelegramBatch caps one get_updates page (Telegram's own maximum).
	hitlTelegramBatch = 100
	// hitlTaskScan bounds how many tasks per status the loop considers. A HITL
	// backlog this deep is pathological; the oldest are simply picked up on a later
	// pass as newer ones close.
	hitlTaskScan = 100
)

// pollLadder backs off the poll rate as a session goes quiet.
//
// A flat rate is wrong for this in both directions, because a Human-in-the-Loop
// conversation is not steady traffic: it is short bursts of replies separated by
// long silence, and the silence is the POINT — waiting on a person is what the node
// is for. Polling every two seconds for the whole of it means a session parked on a
// Friday costs ~110,000 gateway calls by Monday, essentially all of them returning
// an empty list. Polling slowly throughout would instead make an active
// back-and-forth feel broken.
//
// So the rate follows the conversation: fast while someone is talking, backing off
// to hitlTelegramSlowest while nobody is. The same weekend costs ~7,000 calls, and a
// live exchange still turns around in two seconds.
//
// The ladder is read in order; the first step whose `quietFor` the session is still
// inside wins.
var pollLadder = []struct {
	quietFor time.Duration
	every    time.Duration
}{
	// Just said something — a reply is likely imminent.
	{quietFor: time.Minute, every: hitlTelegramFastest},
	// Still plausibly at their phone, composing.
	{quietFor: 5 * time.Minute, every: 5 * time.Second},
	// Drifted off, but recently enough to come back to it.
	{quietFor: 30 * time.Minute, every: 15 * time.Second},
}

// HitlTelegramBridge runs the poll loop. There is one per process, started from
// app.go after the inflow backend is up — closing a session resumes a parked
// flow, which needs the runtime.
type HitlTelegramBridge struct {
	store repository.Store
	// wake is a 1-buffered coalescing signal: a flow that just parked at a
	// Telegram node nudges the loop so the person hears from the bot now rather
	// than up to hitlTelegramIdle later. Many nudges collapse into one pass.
	wake chan struct{}
	// lastActivity is when a live session last showed signs of life — the bot asked
	// something, or a person answered. It drives pollLadder. It is deliberately
	// global rather than per bot: the loop is a single goroutine with one sleep, so
	// there is only one rate to choose, and on a single-tenant install the bots are
	// few. Only the loop goroutine touches it.
	lastActivity time.Time
	// warned remembers the last problem reported per task, so a failure that
	// repeats every tick is surfaced once rather than every two seconds — and a
	// DIFFERENT failure on the same task still gets through. Only the loop
	// goroutine touches it.
	warned map[string]string
}

// theHitlTelegramBridge is the process-wide instance the svc handler nudges.
var theHitlTelegramBridge *HitlTelegramBridge

// StartHitlTelegramBridge creates the bridge and starts its loop. Safe to call
// when no Telegram node exists anywhere: the loop finds no tasks, polls nothing,
// and sleeps.
func StartHitlTelegramBridge(ctx context.Context, store repository.Store) *HitlTelegramBridge {
	b := &HitlTelegramBridge{store: store, wake: make(chan struct{}, 1), warned: map[string]string{}}
	theHitlTelegramBridge = b
	go b.run(ctx)
	fmt.Println("hitl telegram bridge: started")
	return b
}

// NotifyHitlTelegram tells the bridge a Telegram task was just recorded, so the
// session opens immediately instead of on the next idle tick. No-op when no
// bridge is running (the inflow runtime disabled, or a test).
func NotifyHitlTelegram() {
	if theHitlTelegramBridge == nil {
		return
	}
	select {
	case theHitlTelegramBridge.wake <- struct{}{}:
	default:
	}
}

func (b *HitlTelegramBridge) run(ctx context.Context) {
	for {
		live := b.pass(ctx)
		delay := hitlTelegramIdle
		if live {
			delay = b.pollDelay()
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-b.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// pass runs one cycle: open any session that has not spoken yet, then read each
// bound bot's new updates and answer them. It reports whether any Telegram
// session is live, which is what picks the next sleep.
//
// Nothing here is fatal. A gateway that is down, a bot whose token was revoked, a
// model that errors — each is logged against its task and retried on the next
// pass, because the person on the other end has not gone anywhere.
func (b *HitlTelegramBridge) pass(ctx context.Context) bool {
	live := b.liveTasks(ctx)
	// Forget what was reported about sessions that are no longer live, so the map
	// tracks the open ones rather than every one the process has ever seen — and a
	// problem that comes back on a NEW task is reported again.
	b.pruneWarnings(live)
	if len(live) == 0 {
		return false
	}

	deliverable := b.deliverable(live)
	if len(deliverable) == 0 {
		return false
	}
	// On the first pass after a restart there is no in-memory activity to go on, so
	// take it from the sessions themselves: a task touched ten seconds ago resumes at
	// the fast rate, one untouched for three days resumes at the slow one. Guessing
	// "active" instead would poll every two seconds for every stale session the
	// install has ever parked.
	if b.lastActivity.IsZero() {
		for _, task := range deliverable {
			if at := time.UnixMilli(task.UpdatedAt); at.After(b.lastActivity) {
				b.lastActivity = at
			}
		}
	}

	// Sessions that have never spoken: deliver the facilitator's opening turn.
	for _, task := range deliverable {
		if task.Telegram.Opened {
			continue
		}
		if err := b.openSession(ctx, task); err != nil {
			b.report(task, "could not be delivered", err)
			continue
		}
		// A question was just put to someone: poll fast, they may answer at once.
		b.touch()
		b.clearWarning(task.ID)
	}

	// Re-read after opening so the cursors just written are the ones we poll from.
	for _, group := range groupByAccount(b.deliverable(b.liveTasks(ctx))) {
		if err := b.drain(ctx, group); err != nil {
			fmt.Printf("hitl telegram: poll %s: %v\n", group.label(), err)
		}
	}
	return true
}

// liveTasks lists the Telegram tasks a session can still be held on — open or
// answered, never closed. Both statuses matter: `answered` only means every
// question raised so far carries an answer, and the person may well keep talking.
//
// Tasks that cannot be delivered are included, so `pass` can report them and
// remember it has (see deliverable): a Telegram node saved without a chat is
// exactly the case that must not be dropped silently.
func (b *HitlTelegramBridge) liveTasks(ctx context.Context) []*models.HumanTask {
	var out []*models.HumanTask
	for _, status := range []models.HumanTaskStatus{models.HumanTaskOpen, models.HumanTaskAnswered} {
		items, _, err := b.store.HumanTasks().List(ctx, repository.ListParams{
			Status: string(status),
			Limit:  hitlTaskScan,
		})
		if err != nil {
			fmt.Printf("hitl telegram: list %s tasks: %v\n", status, err)
			continue
		}
		for i := range items {
			if items[i].Channel != models.HumanTaskTelegram {
				continue
			}
			task := items[i]
			out = append(out, &task)
		}
	}
	return out
}

// deliverable narrows live sessions to the ones that have somewhere to go, and
// reports the ones that do not. A binding with no chat is a design-time mistake
// nothing at run time can fix, so it is said once per task rather than retried in
// silence.
func (b *HitlTelegramBridge) deliverable(live []*models.HumanTask) []*models.HumanTask {
	out := make([]*models.HumanTask, 0, len(live))
	for _, task := range live {
		if task.Telegram == nil || strings.TrimSpace(task.Telegram.ChatID) == "" {
			b.report(task, "has no Telegram chat configured", nil)
			continue
		}
		out = append(out, task)
	}
	return out
}

// ---- opening a session ------------------------------------------------------

// openSession delivers the facilitator's first turn to the chat.
//
// The order matters. The cursor is established BEFORE the bot speaks: everything
// already sitting on the bot's update stream belongs to whatever happened before
// this flow reached the node, and answering it would be answering questions that
// had not been asked. Then the model produces the opening turn, it is sent, and
// only a send that actually landed marks the session opened — so a failed send is
// retried on the next pass instead of leaving a silent session waiting forever.
func (b *HitlTelegramBridge) openSession(ctx context.Context, task *models.HumanTask) error {
	cfg, err := hitl.ChatConfig(ctx, b.store, task)
	if err != nil {
		return err
	}
	// Refuse to open a session this bot could never hear the answer to. A webhook
	// disables polling silently, so without this the bot would ask its question, the
	// person would reply, and the run would wait for ever on an answer Telegram
	// delivered somewhere else. Better to park unopened with a named reason — which
	// `report` surfaces in the app — than to waste someone's time answering into a
	// void.
	if wh, err := hitl.TelegramWebhook(ctx, b.store, task.Telegram); err != nil {
		return fmt.Errorf("check webhook status: %w", err)
	} else if wh.Active() {
		return fmt.Errorf("%s", wh.WebhookConflict())
	}

	cursor, err := hitl.TelegramLatestUpdateID(ctx, b.store, task.Telegram)
	if err != nil {
		return fmt.Errorf("establish update cursor: %w", err)
	}

	// The thread is empty, so the model is kicked with a transient user nudge that
	// is never stored — same as the in-app /start.
	reply, err := llm.Chat(ctx, cfg, hitl.BuildMessages(task, hitl.OpeningNudge))
	if err != nil {
		return fmt.Errorf("facilitator opening turn: %w", err)
	}
	// State the controls ourselves rather than trusting the model to have done it.
	// Telegram will not show them in its command menu, so an opening turn that forgot
	// to mention /done leaves the person with no way to end the session at all.
	reply += hitl.OpeningFooter()
	if err := hitl.TelegramSendMessage(ctx, b.store, task.Telegram, reply); err != nil {
		return err
	}

	task.Telegram.Cursor = cursor
	task.Telegram.Opened = true
	updated, err := b.store.HumanTasks().AppendMessage(ctx, task.ID, models.HumanTaskMessage{Role: "assistant", Text: reply})
	if err != nil {
		return err
	}
	// AppendMessage re-read the row, so carry the cursor/opened flags onto what it
	// returned before persisting them.
	updated.Telegram = task.Telegram
	if err := b.store.HumanTasks().Upsert(ctx, updated); err != nil {
		return err
	}
	fmt.Printf("hitl telegram: opened session %s in chat %s (cursor %d)\n", task.ID, task.Telegram.ChatID, cursor)
	wslog.Emit("hitl.message", updated)
	return nil
}

// ---- reading the person's replies -------------------------------------------

// accountGroup is the set of live tasks sharing one bound bot. Updates are
// per-bot, not per-task, so a group is the unit of polling: one get_updates call
// serves every session that bot is holding.
type accountGroup struct {
	connection string
	alias      string
	tasks      []*models.HumanTask
}

func (g accountGroup) label() string {
	if g.alias == "" {
		return "default bot"
	}
	return g.alias
}

// groupByAccount buckets tasks by the bot they are bound to.
func groupByAccount(tasks []*models.HumanTask) []accountGroup {
	index := map[string]int{}
	var groups []accountGroup
	for _, t := range tasks {
		key := t.Telegram.Connection + "\x00" + t.Telegram.Alias
		if i, ok := index[key]; ok {
			groups[i].tasks = append(groups[i].tasks, t)
			continue
		}
		index[key] = len(groups)
		groups = append(groups, accountGroup{
			connection: t.Telegram.Connection,
			alias:      t.Telegram.Alias,
			tasks:      []*models.HumanTask{t},
		})
	}
	return groups
}

// drain reads one bot's new updates and runs a turn for every one that belongs to
// a session it is holding.
//
// The poll starts from the LOWEST cursor in the group, because a session opened a
// moment ago sits further along the stream than one opened an hour ago, and
// acknowledging at the newest would drop the older session's answers. Re-reading
// an update some other session already handled is harmless: each task only acts
// on updates past its own cursor.
func (b *HitlTelegramBridge) drain(ctx context.Context, group accountGroup) error {
	after := group.tasks[0].Telegram.Cursor
	for _, t := range group.tasks {
		if t.Telegram.Cursor < after {
			after = t.Telegram.Cursor
		}
	}

	updates, err := hitl.TelegramUpdates(ctx, b.store, group.tasks[0].Telegram, after, hitlTelegramBatch)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	high := after
	for _, up := range updates {
		if up.UpdateID > high {
			high = up.UpdateID
		}
		// Note the chat in the recipient directory whether or not it belongs to a
		// session. An unrouted message is usually someone saying hello to the bot for
		// the first time — which is exactly the moment a Telegram bot becomes able to
		// reach them, and the only moment anything can record it. Missing it would
		// leave them unpickable on the node for good.
		b.remember(ctx, group, up)

		task := routeUpdate(group.tasks, up)
		if task == nil {
			continue
		}
		// Someone answered. Whether the turn then succeeds or fails, the session is
		// live, so go back to the fast rate.
		b.touch()
		if err := b.handleUpdate(ctx, task, up); err != nil {
			b.report(task, fmt.Sprintf("failed on update %d", up.UpdateID), err)
			continue
		}
		b.clearWarning(task.ID)
	}

	// Acknowledge the whole page on every session this bot holds, including the
	// updates that matched no chat: they are never going to match, and leaving
	// them unacknowledged would make Telegram redeliver them on every pass.
	for _, t := range group.tasks {
		if t.Telegram.Cursor >= high {
			continue
		}
		if err := b.saveCursor(ctx, t.ID, high); err != nil {
			fmt.Printf("hitl telegram: save cursor for %s: %v\n", t.ID, err)
			continue
		}
		t.Telegram.Cursor = high
	}
	return nil
}

// routeUpdate picks the session an incoming message belongs to: the live task
// bound to that chat, most recently active first when several are (two flows can
// legitimately be asking the same person about different things — the newest
// question is the one the person is answering).
func routeUpdate(tasks []*models.HumanTask, up hitl.TelegramUpdate) *models.HumanTask {
	var best *models.HumanTask
	for _, t := range tasks {
		if up.UpdateID <= t.Telegram.Cursor || !up.MatchesChat(t.Telegram.ChatID) {
			continue
		}
		// A session that has not spoken yet has nothing to reply to.
		if !t.Telegram.Opened {
			continue
		}
		if best == nil || t.UpdatedAt > best.UpdatedAt {
			best = t
		}
	}
	return best
}

// handleUpdate runs one turn of the conversation: record what the person said,
// then either close the session (they typed /done) or answer them.
func (b *HitlTelegramBridge) handleUpdate(ctx context.Context, task *models.HumanTask, up hitl.TelegramUpdate) error {
	text := up.Body()
	if text == "" {
		// A sticker, a photo with no caption, a location — nothing the facilitator can
		// read. Tell the person rather than ignoring them, and record nothing: there
		// is no turn here to transcribe, and inventing "(sent a photo)" would put
		// words in the thread the model would then have to reason about.
		return hitl.TelegramSendMessage(ctx, b.store, task.Telegram,
			"I can only read text here. Could you type your answer out?")
	}

	// Record the human turn first so it is durable even if the model call fails.
	updated, err := b.store.HumanTasks().AppendMessage(ctx, task.ID, models.HumanTaskMessage{Role: "human", Text: text})
	if err != nil {
		return err
	}
	updated.Telegram = task.Telegram
	wslog.Emit("hitl.message", updated)

	// Commands stand in for the UI the person does not have, so they are answered
	// from the record here rather than handed to the model — see hitl/commands.go.
	// An unknown command gets the list back instead of being answered as prose:
	// someone typing /finish is reaching for a control, not making a remark, and
	// there is no menu for them to look it up in.
	switch cmd := hitl.ParseCommand(text); {
	case cmd == hitl.CmdDone:
		return b.finish(ctx, updated)
	case cmd == hitl.CmdStatus:
		return b.say(ctx, updated, hitl.StatusMessage(updated))
	case cmd == hitl.CmdHelp:
		return b.say(ctx, updated, hitl.HelpMessage(updated))
	case hitl.LooksLikeCommand(text):
		return b.say(ctx, updated, hitl.UnknownCommandMessage(text))
	}

	// From here on the person is owed an answer. Their turn is already recorded and
	// the cursor advances at the end of the page either way (re-running the model on
	// the same message every pass would be worse than one apology), so a failure
	// must not leave the chat silent — the operator sees the log, the person sees
	// that something broke and that they can retry.
	cfg, err := hitl.ChatConfig(ctx, b.store, updated)
	if err != nil {
		return b.apologise(ctx, task, err)
	}
	reply, err := llm.Chat(ctx, cfg, hitl.BuildMessages(updated, ""))
	if err != nil {
		return b.apologise(ctx, task, fmt.Errorf("facilitator reply: %w", err))
	}
	if err := hitl.TelegramSendMessage(ctx, b.store, task.Telegram, reply); err != nil {
		return err
	}
	stored, err := b.store.HumanTasks().AppendMessage(ctx, task.ID, models.HumanTaskMessage{Role: "assistant", Text: reply})
	if err != nil {
		return err
	}
	stored.Telegram = task.Telegram
	wslog.Emit("hitl.message", stored)
	return nil
}

// apologise tells the person their turn landed but could not be answered, and
// returns the original error so the caller still logs what actually happened. A
// failed apology is folded into that error rather than replacing it — the cause is
// what an operator needs.
func (b *HitlTelegramBridge) apologise(ctx context.Context, task *models.HumanTask, cause error) error {
	if err := hitl.TelegramSendMessage(ctx, b.store, task.Telegram,
		"Sorry — I got your message but could not answer it just now. Please send it again in a moment."); err != nil {
		return fmt.Errorf("%w (and could not tell the chat: %v)", cause, err)
	}
	return cause
}

// say sends the bridge's own message to the chat and records it on the task as an
// assistant turn, so the in-app transcript is the whole conversation — a person
// reading it later should not find a gap where the bot answered /status.
func (b *HitlTelegramBridge) say(ctx context.Context, task *models.HumanTask, text string) error {
	if err := hitl.TelegramSendMessage(ctx, b.store, task.Telegram, text); err != nil {
		return err
	}
	stored, err := b.store.HumanTasks().AppendMessage(ctx, task.ID, models.HumanTaskMessage{Role: "assistant", Text: text})
	if err != nil {
		return err
	}
	stored.Telegram = task.Telegram
	wslog.Emit("hitl.message", stored)
	return nil
}

// remember records the chat an update came from in the recipient directory, so it
// can be picked on a node later. Best-effort: this is a convenience for the next
// designer and must never interfere with the conversation in progress.
func (b *HitlTelegramBridge) remember(ctx context.Context, group accountGroup, up hitl.TelegramUpdate) {
	if up.Message == nil {
		return
	}
	binding := &models.TelegramBinding{Connection: group.connection, Alias: group.alias}
	if err := hitl.RecordRecipient(ctx, b.store, binding, up.Message.Chat, up.Message.Date); err != nil {
		fmt.Printf("hitl telegram: record recipient for %s: %v\n", group.label(), err)
	}
}

// finish closes the session from the chat — the messenger equivalent of the app's
// Close task button, and the same consequence: the conversation's outcome is
// bound into the run's context under the node's key, and a parked flow resumes
// from the nodes it stopped at.
//
// The person is told what happened either way. A resume that fails is reported to
// them and logged, because from the chat's point of view the session IS over —
// re-answering would not help, and an operator has to look at the run.
func (b *HitlTelegramBridge) finish(ctx context.Context, task *models.HumanTask) error {
	closed, err := b.store.HumanTasks().Close(ctx, task.ID)
	if err != nil {
		return err
	}
	closed.Telegram = task.Telegram
	wslog.Emit("hitl.message", closed)

	if err := WriteHumanTaskContext(ctx, b.store, closed); err != nil {
		fmt.Printf("hitl telegram: write context for %s: %v\n", closed.ID, err)
	}
	// A nil run with a nil error is an ordinary outcome, not a silence to explain
	// away: a `continue` node never parked its flow, and a parked node with no
	// outbound edges was the end of the line. Only claim the flow moved when it did.
	// The closing courtesy below is sent, not recorded: the task is already closed
	// and its transcript has been bound into the run's context, so appending a turn
	// after closedAt would only muddy both. The person's /done is the last thing in
	// the thread, which is the truthful end of it.
	run, err := ResumeHumanTask(ctx, b.store, closed)
	switch {
	case err != nil:
		fmt.Printf("hitl telegram: resume %s: %v\n", closed.ID, err)
		return hitl.TelegramSendMessage(ctx, b.store, task.Telegram,
			"Thanks — your answers are saved, but the workflow could not be released automatically. Someone will need to look at the run.")
	case run != nil:
		return hitl.TelegramSendMessage(ctx, b.store, task.Telegram,
			"Thanks — that is everything I needed. The workflow is continuing from here.")
	default:
		return hitl.TelegramSendMessage(ctx, b.store, task.Telegram,
			"Thanks — that is everything I needed. Your answers are recorded and we are done here.")
	}
}

// touch marks a live session as active, which resets the poll rate to the fastest
// rung of pollLadder.
func (b *HitlTelegramBridge) touch() { b.lastActivity = time.Now() }

// pollDelay picks the next poll interval from how long the conversation has been
// quiet. See pollLadder for why the rate follows the conversation rather than a
// fixed cadence.
func (b *HitlTelegramBridge) pollDelay() time.Duration {
	quiet := time.Since(b.lastActivity)
	for _, step := range pollLadder {
		if quiet < step.quietFor {
			return step.every
		}
	}
	return hitlTelegramSlowest
}

// report surfaces a problem with one session: logged for the operator's terminal
// and pushed as a warning notification so it is visible in the app, which is the
// only place anyone is watching. A Telegram session that cannot be delivered is
// otherwise completely silent — the person never hears from the bot, the task sits
// open, and nothing says why.
//
// The loop retries every tick, so the same problem would notify every two seconds.
// It is reported once per task and again only if the problem CHANGES, which is
// exactly when it is worth hearing about a second time.
func (b *HitlTelegramBridge) report(task *models.HumanTask, what string, cause error) {
	msg := what
	if cause != nil {
		msg = what + ": " + cause.Error()
	}
	if b.warned[task.ID] == msg {
		return
	}
	b.warned[task.ID] = msg
	fmt.Printf("hitl telegram: task %s %s\n", task.ID, msg)
	wslog.Notify(wslog.LevelWarning, "Telegram human task "+what,
		fmt.Sprintf("%q — %s", task.Title, strings.TrimPrefix(msg, what+": ")))
}

// clearWarning forgets a task's last problem after a pass that worked, so a
// recurrence is reported again rather than swallowed as a duplicate.
func (b *HitlTelegramBridge) clearWarning(taskID string) {
	delete(b.warned, taskID)
}

// pruneWarnings drops what is remembered about tasks that are no longer live, so
// the map tracks the open sessions rather than every session the process has seen.
func (b *HitlTelegramBridge) pruneWarnings(live []*models.HumanTask) {
	if len(b.warned) == 0 {
		return
	}
	alive := make(map[string]struct{}, len(live))
	for _, t := range live {
		alive[t.ID] = struct{}{}
	}
	for id := range b.warned {
		if _, ok := alive[id]; !ok {
			delete(b.warned, id)
		}
	}
}

// saveCursor persists a task's acknowledged update id, re-reading the row so a
// turn recorded in between is not overwritten.
func (b *HitlTelegramBridge) saveCursor(ctx context.Context, taskID string, cursor int64) error {
	task, err := b.store.HumanTasks().GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Telegram == nil || task.Telegram.Cursor >= cursor {
		return nil
	}
	task.Telegram.Cursor = cursor
	return b.store.HumanTasks().Upsert(ctx, task)
}
