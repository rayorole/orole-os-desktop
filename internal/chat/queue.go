package chat

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// turn is one unit of work for a bot's worker: answer a stored user message.
type turn struct {
	userMsgID string
	mode      string
	// asstID is set when regenerating: the assistant message is reset and reused in place.
	asstID string

	// responder is the bot that answers instead of the conversation's own bot (an @mention); "".
	responder string
	// skills are skill names the user asked the answering bot to use.
	skills []string
	// integrations are MCP server ids the user asked the answering bot to use.
	integrations []string

	// Delegation (ask_bot). All zero for turns the user started.
	depth  int                  // ask_bot depth of the run
	chain  []string             // bots waiting on this run's answer, outermost first
	extra  string               // system prompt addendum for the run
	result chan<- handoffResult // receives the outcome exactly once; buffered so it never blocks
	// automations is the automation chain behind this turn (Message.Automations); empty for user turns.
	automations []string
	// callID puts the turn in a phone call's thread: the message it answers and its reply carry it (Message.CallID)
	// and are published as call events only, and the turn announces no chat.run. It runs like any other turn of the
	// bot's queue. submit copies it from the message the turn answers (its only source); "" for chat turns.
	callID string
}

// handoffResult is how a delegated turn ended.
type handoffResult struct {
	text    string // final assistant text
	status  string // a Status* value
	errText string // user-facing failure text when status is not complete
}

// deliver reports the outcome of a delegated turn to the bot that asked. It never blocks, and does
// nothing for turns that were not delegated.
func (t turn) deliver(r handoffResult) {
	if t.result == nil {
		return
	}
	select {
	case t.result <- r:
	default:
	}
}

// runState exists in Manager.runs while a bot (or draft) has an active run. Further sends are
// queued here and processed in order after the current run, one run per bot at a time.
type runState struct {
	botID  string             // set on phone-call runs ("call:<id>"), which belong to a bot but are not its chat run
	cancel context.CancelFunc // cancels the active turn; nil between turns
	active string             // user message id of the active turn
	queue  []turn
	done   chan struct{} // closed when the worker exits
	// paused holds the queue: the worker finishes the active turn, then waits instead of starting the next one.
	paused bool
	// wake nudges a worker that is waiting on a paused queue to look again (resume, cancel, a drained queue).
	wake chan struct{}
	// shown is set once the chat was told about this worker (a chat turn started, or the queue was paused or
	// resumed); its end is then announced as well. A worker that only ran call thread turns stays out of the chat.
	shown bool
}

// poke wakes the worker if it is waiting. It never blocks and may be called without m.mu.
func (st *runState) poke() {
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

// Send stores the user message immediately and starts a run, or queues the message behind the
// active run (it is stored with status pending until its turn starts). A retried ClientNonce
// returns the stored message instead of sending twice.
func (m *Manager) Send(ctx context.Context, req SendRequest) (Message, error) {
	if !utf8.ValidString(req.Text) {
		return Message{}, invalidf("The message is not valid text.")
	}
	if len(req.Text) > MaxMessageTextLen {
		return Message{}, invalidf("Messages are limited to %d KiB of text.", MaxMessageTextLen>>10)
	}
	text := strings.TrimRight(req.Text, " \t\r\n")
	if strings.TrimSpace(text) == "" && len(req.AttachmentIDs) == 0 {
		return Message{}, invalidf("Write a message or attach a file.")
	}
	if len(req.AttachmentIDs) > MaxAttachmentsPerMsg {
		return Message{}, invalidf("A message can have at most %d attachments.", MaxAttachmentsPerMsg)
	}
	switch req.Mode {
	case ModeChat:
	case ModeResearch:
		if strings.TrimSpace(text) == "" {
			return Message{}, invalidf("Research needs a question.")
		}
		if len(req.AttachmentIDs) > 0 {
			return Message{}, invalidf("Research takes a question only; remove the attachments or send a normal message.")
		}
	default:
		return Message{}, invalidf("Unknown mode %q.", req.Mode)
	}
	ids := dedupe(req.AttachmentIDs)
	mentions, skills, integrations, err := m.checkMentions(ctx, req)
	if err != nil {
		return Message{}, err
	}

	unlock := m.lockBot(req.BotID)
	defer unlock()
	if m.isClosed() {
		return Message{}, ErrClosed
	}
	if req.ClientNonce != "" {
		m.mu.Lock()
		id, ok := m.nonces.get(req.BotID, req.ClientNonce)
		m.mu.Unlock()
		if ok {
			if msg, err := m.store.getMessage(ctx, id); err == nil {
				return msg, nil
			}
		}
	}
	if _, err := m.store.getBot(ctx, req.BotID, false); err != nil {
		return Message{}, err
	}
	// One turn per answering bot: the first mention (or the conversation's own bot) and one for each further mention.
	turns := []turn{{mode: req.Mode, skills: skills, integrations: integrations}}
	if len(mentions) > 0 {
		turns[0].responder = mentions[0]
		for _, id := range mentions[1:] {
			turns = append(turns, turn{responder: id, skills: skills, integrations: integrations})
		}
	}
	msg, err := m.submit(ctx, Message{BotID: req.BotID, Role: "user", Author: "user"}, text, ids, turns...)
	if err != nil {
		return Message{}, err
	}
	if req.ClientNonce != "" {
		m.mu.Lock()
		m.nonces.put(req.BotID, req.ClientNonce, msg.ID)
		m.mu.Unlock()
	}
	return msg, nil
}

// submit stores msg (a user message of msg.BotID) with the given text and attachments, publishes it
// and queues turns (each answers the message) behind the bot's active run, or starts the bot's worker
// when it is idle. The message is stored pending while it waits behind an active run. msg.CallID puts
// the message, and so the turns answering it, in that call's thread.
//
// The caller holds lockBot(msg.BotID) and has verified that the bot exists. That lock is what keeps
// the order of messages and turns identical for one bot, so the SQLite work here runs outside the
// global m.mu, which is only held to read and change the in-memory run state. msg.ID, when set, is the
// id to store it under (a follow-up's id is chosen before its hook runs); otherwise a new one is made.
func (m *Manager) submit(ctx context.Context, msg Message, text string, attachmentIDs []string, turns ...turn) (Message, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Message{}, ErrClosed
	}
	st := m.runs[msg.BotID]
	if st != nil && len(st.queue) >= maxQueued {
		m.mu.Unlock()
		return Message{}, busyf("Too many messages are waiting. Wait for the bot to catch up.")
	}
	m.mu.Unlock()
	if msg.ID == "" {
		msg.ID = newID()
	}
	msg.Status, msg.CreatedAt = StatusComplete, m.nowMs()
	if st != nil {
		msg.Status = StatusPending
	}
	if err := m.store.insertUserMessage(ctx, &msg, text, attachmentIDs); err != nil {
		return Message{}, err
	}
	m.publishAny(msg)
	if !m.enqueue(msg, turns...) {
		// The daemon began shutting down while the message was being stored: it will never run.
		if err := m.store.setStatus(context.WithoutCancel(ctx), msg.ID, StatusCanceled, "Not sent: Orole was closed before this message was processed."); err != nil {
			m.log.Warn("chat: cancel message stored during shutdown", "err", err)
		}
		return Message{}, ErrClosed
	}
	return msg, nil
}

// enqueue queues turns that answer the stored message msg behind the bot's active run, or starts the bot's worker
// when it is idle. It reports false when the manager has closed. The caller holds lockBot(msg.BotID).
func (m *Manager) enqueue(msg Message, turns ...turn) bool {
	for i := range turns {
		turns[i].userMsgID, turns[i].callID = msg.ID, msg.CallID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.cancelIdleLocked(msg.BotID)
	// Only a bot-lock holder starts runs, so a run seen before is either still there or has finished
	// meanwhile; in the second case a pending message is answered by a fresh worker, which marks it complete.
	if cur := m.runs[msg.BotID]; cur != nil {
		cur.queue = append(cur.queue, turns...)
		if msg.CallID == "" && len(turns) > 0 && !cur.shown {
			// A chat message waits behind a call thread answer the chat was never told about: show the bot busy
			// (and stoppable) now. A worker always has work running or queued, and one the chat was never told
			// about was never paused, so it is running even before it pops its first turn (cancel still nil).
			// Emitted under m.mu so it precedes everything the worker announces later.
			cur.shown = true
			m.emit("chat.run", RunState{BotID: msg.BotID, Running: true})
		}
	} else {
		m.startLocked(msg.BotID, turns...)
	}
	return true
}

// record stores msg (a user message of msg.BotID) with the given text as complete and publishes it, without a turn to
// answer it: a follow-up that was handed to the live call it came from. msg.ID, when set, is the id to store it under.
// The caller holds lockBot(msg.BotID).
func (m *Manager) record(ctx context.Context, msg Message, text string) (Message, error) {
	if m.isClosed() {
		return Message{}, ErrClosed
	}
	if msg.ID == "" {
		msg.ID = newID()
	}
	msg.Status, msg.CreatedAt = StatusComplete, m.nowMs()
	if err := m.store.insertUserMessage(ctx, &msg, text, nil); err != nil {
		return Message{}, err
	}
	m.publishAny(msg)
	return msg, nil
}

func dedupe(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !containsString(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// startLocked launches the worker for a bot, first answering turns in order. The caller holds m.mu
// and has verified there is no run.
func (m *Manager) startLocked(botID string, turns ...turn) {
	st := &runState{done: make(chan struct{}), wake: make(chan struct{}, 1), queue: turns, shown: len(turns) > 0 && turns[0].callID == ""}
	m.runs[botID] = st
	m.wg.Add(1)
	go m.pump(botID, st)
}

// pump processes the bot's queue in FIFO order, then exits.
func (m *Manager) pump(botID string, st *runState) {
	defer m.wg.Done()
	ran := false
	for {
		m.mu.Lock()
		if st.paused && len(st.queue) > 0 && !m.closed && m.ctx.Err() == nil {
			st.shown = true
			m.mu.Unlock()
			m.emit("chat.run", RunState{BotID: botID, Paused: true})
			select {
			case <-st.wake:
			case <-m.ctx.Done():
			}
			continue
		}
		if len(st.queue) == 0 || m.closed {
			left := st.queue // only after a shutdown: these turns will never run
			st.queue = nil
			delete(m.runs, botID)
			st.cancel, st.active = nil, ""
			close(st.done)
			// Published before m.mu is released: every later run of this bot starts under m.mu, so its chat.run
			// running=true can never be published ahead of this running=false (clients would show the bot idle mid-run).
			// Publish never blocks or calls back into the Manager.
			if st.shown {
				m.emit("chat.run", RunState{BotID: botID})
			}
			m.mu.Unlock()
			for _, t := range left {
				t.deliver(handoffResult{status: StatusCanceled, errText: "the daemon is shutting down"})
			}
			if ran {
				m.armIdle(botID)
			}
			return
		}
		t := st.queue[0]
		st.queue = st.queue[1:]
		ctx, cancel := context.WithCancel(m.ctx)
		st.cancel, st.active = cancel, t.userMsgID
		if t.callID == "" {
			st.shown = true
		}
		m.mu.Unlock()
		m.runTurn(ctx, botID, t)
		cancel()
		ran = true
	}
}

// Regenerate resets the latest assistant reply in place (same message id) and runs it again.
func (m *Manager) Regenerate(ctx context.Context, req RegenerateRequest) error {
	unlock := m.lockBot(req.BotID)
	defer unlock()
	m.mu.Lock()
	closed, running, onCall := m.closed, m.runs[req.BotID] != nil, m.onCallLocked(req.BotID)
	m.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if running {
		return busyf("The bot is still replying. Stop it first, then regenerate.")
	}
	if onCall {
		return busyf("The bot is answering on a phone call. Try again after the call.")
	}
	if _, err := m.store.getBot(ctx, req.BotID, false); err != nil {
		return err
	}
	last, err := m.store.lastMessage(ctx, req.BotID)
	if err != nil {
		return err
	}
	if last.Role != "assistant" || last.ID != req.MessageID {
		return invalidf("Only the latest reply can be regenerated.")
	}
	if slices.ContainsFunc(last.Parts, func(p MsgPart) bool { return p.Type == PartCall }) {
		return invalidf("A phone call card can't be regenerated.")
	}
	user, err := m.store.userMessageBefore(ctx, req.BotID, last.Seq)
	if err != nil {
		return err
	}
	mode := ModeChat
	for _, p := range last.Parts {
		if p.Type == PartReport {
			mode = ModeResearch
		}
	}
	responder := strings.TrimPrefix(last.Author, "bot:")
	if !strings.HasPrefix(last.Author, "bot:") || responder == req.BotID {
		responder = ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.cancelIdleLocked(req.BotID)
	m.startLocked(req.BotID, turn{userMsgID: user.ID, mode: mode, asstID: last.ID, responder: responder})
	return nil
}

// onCallLocked reports whether a phone call turn (run key call:<id>) is running for the bot. m.mu must be held.
func (m *Manager) onCallLocked(botID string) bool {
	for key, st := range m.runs {
		if strings.HasPrefix(key, callPrefix) && st.botID == botID {
			return true
		}
	}
	return false
}

// Cancel stops the active run of a bot (or draft "draft:<id>") and drops messages still queued
// behind it: stopping means stopping, so queued messages are marked canceled rather than sent.
func (m *Manager) Cancel(ctx context.Context, botID string) error {
	m.mu.Lock()
	st := m.runs[botID]
	if st == nil {
		m.mu.Unlock()
		return nil
	}
	dropped, cancel := st.queue, st.cancel
	st.queue, st.paused = nil, false
	m.mu.Unlock()
	st.poke()
	m.dropQueued(ctx, dropped)
	if cancel != nil {
		cancel()
	}
	return nil
}

// dropQueued cancels turns that were still waiting when their run was stopped.
func (m *Manager) dropQueued(ctx context.Context, turns []turn) {
	for _, t := range turns {
		m.cancelQueued(ctx, t, "Not sent: the reply before it was stopped.")
	}
}

// PauseQueue holds the bot's waiting messages: the active reply finishes, then nothing new starts until
// ResumeQueue. Pausing a bot that is idle is a no-op: there is nothing to hold.
func (m *Manager) PauseQueue(_ context.Context, botID string) error {
	m.mu.Lock()
	st := m.runs[botID]
	if st == nil || st.paused {
		m.mu.Unlock()
		return nil
	}
	st.paused, st.shown = true, true
	running := st.cancel != nil
	m.mu.Unlock()
	m.emit("chat.run", RunState{BotID: botID, Running: running, Paused: true})
	return nil
}

// ResumeQueue lets a paused queue run again.
func (m *Manager) ResumeQueue(_ context.Context, botID string) error {
	m.mu.Lock()
	st := m.runs[botID]
	if st == nil || !st.paused {
		m.mu.Unlock()
		return nil
	}
	st.paused, st.shown = false, true
	running := st.cancel != nil
	m.mu.Unlock()
	st.poke()
	m.emit("chat.run", RunState{BotID: botID, Running: running})
	return nil
}

// CancelQueued drops one message that is still waiting in the bot's queue; the message is kept in the
// conversation, marked canceled. A message that already started, or never waited, is left alone.
func (m *Manager) CancelQueued(ctx context.Context, botID, messageID string) error {
	m.mu.Lock()
	st := m.runs[botID]
	if st == nil {
		m.mu.Unlock()
		return notFoundf("That message is no longer waiting.")
	}
	var dropped []turn
	st.queue = slices.DeleteFunc(st.queue, func(t turn) bool {
		if t.userMsgID != messageID {
			return false
		}
		dropped = append(dropped, t)
		return true
	})
	m.mu.Unlock()
	if len(dropped) == 0 {
		return notFoundf("That message is no longer waiting.")
	}
	st.poke()
	for _, t := range dropped {
		m.cancelQueued(ctx, t, "Canceled before it was sent.")
	}
	return nil
}

// cancelQueued marks the user message of a turn that will never run as canceled, publishes it (in
// its call's thread when it has one) and tells the bot waiting on it, if any.
func (m *Manager) cancelQueued(ctx context.Context, t turn, reason string) {
	t.deliver(handoffResult{status: StatusCanceled, errText: "the task was stopped before it started"})
	if t.userMsgID == "" {
		return
	}
	if err := m.store.setStatus(ctx, t.userMsgID, StatusCanceled, reason); err != nil {
		m.log.Warn("chat: cancel queued message", "err", err)
		return
	}
	if msg, err := m.store.getMessage(ctx, t.userMsgID); err == nil {
		m.publishAny(msg)
	}
}

// callRunsLocked returns the active phone-call turns of a bot. m.mu must be held.
func (m *Manager) callRunsLocked(botID string) []*runState {
	var out []*runState
	for k, st := range m.runs {
		if st.botID == botID && strings.HasPrefix(k, callPrefix) {
			out = append(out, st)
		}
	}
	return out
}

// whenIdle stops the bot's run (dropping its queue), then runs fn while no new run can start.
func (m *Manager) whenIdle(ctx context.Context, botID string, fn func() error) error {
	for {
		unlock := m.lockBot(botID)
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			unlock()
			return ErrClosed
		}
		st := m.runs[botID]
		if st == nil {
			if calls := m.callRunsLocked(botID); len(calls) > 0 {
				// A phone call is mid-turn: stop it and wait, then look again.
				m.mu.Unlock()
				unlock()
				for _, c := range calls {
					c.cancel()
				}
				for _, c := range calls {
					select {
					case <-c.done:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				continue
			}
			// No run exists and only holders of the bot lock can start one, so fn runs with the bot
			// idle without the global lock being held across its SQLite work.
			m.mu.Unlock()
			defer unlock()
			if _, err := m.store.getBot(ctx, botID, false); err != nil {
				return err
			}
			return fn()
		}
		dropped, cancel, done := st.queue, st.cancel, st.done
		st.queue, st.paused = nil, false
		m.mu.Unlock()
		unlock()
		st.poke()
		m.dropQueued(ctx, dropped)
		if cancel != nil {
			cancel()
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---- idle consolidation ----

type idleJob struct{ timer *time.Timer }

// armIdle schedules one Memory.Consolidate call after the bot has been idle for the configured time.
func (m *Manager) armIdle(botID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.memory == nil {
		return
	}
	m.cancelIdleLocked(botID)
	j := &idleJob{}
	j.timer = time.AfterFunc(m.cfg.IdleConsolidateAfter, func() { m.consolidate(botID, j) })
	m.idle[botID] = j
}

func (m *Manager) cancelIdleLocked(botID string) {
	if j := m.idle[botID]; j != nil {
		j.timer.Stop()
		delete(m.idle, botID)
	}
}

func (m *Manager) cancelIdleTimer(botID string) {
	m.mu.Lock()
	m.cancelIdleLocked(botID)
	m.mu.Unlock()
}

func (m *Manager) consolidate(botID string, j *idleJob) {
	m.mu.Lock()
	if m.idle[botID] != j {
		m.mu.Unlock()
		return
	}
	delete(m.idle, botID)
	mem := m.memory
	if m.closed || mem == nil || m.runs[botID] != nil || len(m.callRunsLocked(botID)) > 0 {
		m.mu.Unlock()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	if err := mem.Consolidate(m.ctx, botID); err != nil && m.ctx.Err() == nil {
		m.log.Warn("chat: memory consolidation failed", "bot", botID, "err", err)
	}
}
