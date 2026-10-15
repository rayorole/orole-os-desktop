package daemon

import (
	"context"
	"strings"
	"sync"

	"github.com/rayorole/orole-os-desktop/internal/agents"
	"github.com/rayorole/orole-os-desktop/internal/automations"
	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/events"
	"github.com/rayorole/orole-os-desktop/internal/notifications"
	"github.com/rayorole/orole-os-desktop/internal/policy"
)

// noticeBody bounds the text a notification copies from a task, reply or run; the
// center shows one line and the full text stays where it came from.
const noticeBody = 300

// Refs of approvals: one open "Needs you" entry per wait, resolved when the wait ends.
const (
	refTask     = "task:"         // a code task is in needs-you
	refQuestion = "bot-question:" // a bot asked the user something (ask_user)
	refRun      = "run:"          // a flow run waits at an approval step
	refPolicy   = "policy:"       // a bot, flow, agent or tool waits for a permission decision
)

// appNotices turns what code tasks, bots, flows and permission requests do into
// notification-center entries. Producers call it from their publish callbacks, often
// holding their own locks, so every call only queues a job; one worker runs the jobs in
// order, outside those locks, and may look things up (bot names, approval messages).
// A full queue drops the job rather than stall a producer.
type appNotices struct {
	d      *Daemon
	jobs   chan func()
	done   chan struct{}
	mu     sync.Mutex // guards closed and sends on jobs
	closed bool

	// Worker-owned state.
	runs   map[string]string   // flow run id -> last status seen
	policy map[string]struct{} // permission requests already reported
	msgs   recentIDs           // bot messages already reported in a final state
}

func newAppNotices(d *Daemon) *appNotices {
	n := &appNotices{d: d, jobs: make(chan func(), 256), done: make(chan struct{}), runs: map[string]string{}, policy: map[string]struct{}{}, msgs: newRecentIDs(512)}
	go n.loop()
	return n
}

func (n *appNotices) loop() {
	defer close(n.done)
	for job := range n.jobs {
		job()
	}
}

// close stops the worker after the queued jobs ran.
func (n *appNotices) close() {
	if n == nil {
		return
	}
	n.mu.Lock()
	if !n.closed {
		n.closed = true
		close(n.jobs)
	}
	n.mu.Unlock()
	<-n.done
}

func (n *appNotices) do(job func()) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	select {
	case n.jobs <- job:
	default:
		n.d.Log.Warn("notification queue full; dropped an event")
	}
}

func (n *appNotices) notify(in notifications.Input) {
	if _, err := n.d.Notify(in); err != nil {
		n.d.Log.Warn("notification not recorded", "source", in.Source, "kind", in.Kind, "err", err)
	}
}

// reconcile resolves approvals left open by an earlier daemon whose wait is gone. Run it
// once the agents and automations runtimes have recovered their state.
func (n *appNotices) reconcile() {
	n.do(func() {
		ctx := context.Background()
		refs, err := notifications.New(n.d.DB.DB).OpenRefs(ctx)
		if err != nil || len(refs) == 0 {
			return
		}
		waitingRuns := map[string]bool{}
		if n.d.Automations != nil {
			if list, err := n.d.Automations.Approvals(ctx); err == nil {
				for _, a := range list {
					waitingRuns[a.RunID] = true
				}
			}
		}
		askingBots := map[string]bool{}
		if n.d.Chat != nil {
			if bots, err := n.d.Chat.Bots(ctx); err == nil {
				for _, b := range bots {
					askingBots[b.ID] = b.LastMessage != nil && b.LastMessage.Awaiting
				}
			}
		}
		for _, ref := range refs {
			live := false
			switch {
			case strings.HasPrefix(ref, refTask):
				if n.d.Agents != nil {
					s, err := n.d.Agents.Get(strings.TrimPrefix(ref, refTask))
					live = err == nil && s.Status == agents.StatusNeedsYou
				}
			case strings.HasPrefix(ref, refQuestion):
				live = askingBots[strings.TrimPrefix(ref, refQuestion)]
			case strings.HasPrefix(ref, refRun):
				live = waitingRuns[strings.TrimPrefix(ref, refRun)]
			case strings.HasPrefix(ref, refPolicy):
				// Permission requests live in memory only; none survive a restart.
			case strings.HasPrefix(ref, refTakeover):
				// Takeover requests (ask_user_to_take_over) wait in a bot turn, which a restart ends.
			default:
				live = true // a ref this daemon does not produce: leave it
			}
			if !live {
				n.d.resolveNotification(ref)
			}
		}
	})
}

// --- Code tasks ---

func taskTitle(s agents.Session) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	if t := strings.TrimSpace(firstLine(s.Task)); t != "" {
		return clipTo(t, 80)
	}
	return "Code task"
}

// task reports a task transition published by taskEvents. Tasks a bot started report back
// to that bot, whose reply notifies; only their waits are reported here.
func (n *appNotices) task(topic string, s agents.Session) {
	n.do(func() {
		in := notifications.Input{Source: "task", Title: taskTitle(s), Target: "task:" + s.ID}
		switch topic {
		case events.TopicAgentWaiting:
			in.Kind, in.Ref = notifications.Approval, refTask+s.ID
			in.Body = orDefault(s.Activity, "Waiting for you")
		case events.TopicAgentFailed:
			if s.Owner != "" {
				return
			}
			in.Kind, in.Level = notifications.Failed, notifications.Error
			in.Body = orDefault(s.Error, "The task stopped with an error.")
		case events.TopicAgentFinished:
			if s.Owner != "" {
				return
			}
			in.Kind, in.Level = notifications.Finished, notifications.Success
			in.Body = orDefault(firstLine(s.Result), orDefault(firstLine(s.LastMessage), "Done"))
		default:
			return
		}
		in.Body = clipTo(in.Body, noticeBody)
		n.notify(in)
	})
}

// taskResolved ends a task's open approval: it left needs-you or was removed.
func (n *appNotices) taskResolved(id string) {
	n.do(func() { n.d.resolveNotification(refTask + id) })
}

// --- Bots ---

func (n *appNotices) botName(id string) string {
	if n.d.Chat != nil {
		if b, err := n.d.Chat.Bot(context.Background(), id); err == nil && strings.TrimSpace(b.Name) != "" {
			return b.Name
		}
	}
	return "Bot"
}

// chatMessage reports a bot's final reply, failure or question, and resolves the question
// once the user writes to that bot.
func (n *appNotices) chatMessage(event string, payload any) {
	if n == nil || event != "chat.message" {
		return
	}
	m, ok := payload.(chat.Message)
	if !ok || m.BotID == "" {
		return
	}
	if m.Role == "user" {
		if m.Author == "user" {
			n.do(func() { n.d.resolveNotification(refQuestion + m.BotID) })
		}
		return
	}
	// A reply's author is the bot that wrote it; "bot:draft" is the bot builder's preview chat.
	if m.Role != "assistant" || m.Author == "bot:draft" {
		return
	}
	if m.Status != chat.StatusComplete && m.Status != chat.StatusError {
		return
	}
	n.do(func() {
		if !n.msgs.add(m.ID) {
			return
		}
		in := notifications.Input{Source: "bot", Title: n.botName(m.BotID), Target: "bot:" + m.BotID}
		switch {
		case m.Status == chat.StatusError:
			in.Kind, in.Level = notifications.Failed, notifications.Error
			in.Body = orDefault(m.Error, "The reply stopped with an error.")
		case question(m) != "":
			in.Kind, in.Ref = notifications.Approval, refQuestion+m.BotID
			in.Body = question(m)
		default:
			in.Kind = notifications.Reply
			in.Body = orDefault(firstLine(m.Text()), "Replied")
		}
		in.Body = clipTo(in.Body, noticeBody)
		n.notify(in)
	})
}

// question is the text of the message's ask_user question, if it has one.
func question(m chat.Message) string {
	for _, p := range m.Parts {
		if p.Type == chat.PartQuestion {
			return orDefault(p.Text, "Has a question for you")
		}
	}
	return ""
}

// --- Flows ---

// run reports flow run status changes. The engine reports a run on every journal write, so
// only status transitions count. Successful runs notify only when the user started them:
// scheduled and event-driven flows would otherwise notify on every tick.
func (n *appNotices) run(r automations.Run) {
	n.do(func() {
		prev, seen := n.runs[r.ID]
		if seen && prev == r.Status {
			return
		}
		n.runs[r.ID] = r.Status
		if prev == "waiting-approval" {
			n.d.resolveNotification(refRun + r.ID)
		}
		in := notifications.Input{Source: "automation/" + r.AutomationID, Title: orDefault(r.Name, "Flow"), Target: "automation:" + r.AutomationID}
		switch r.Status {
		case "waiting-approval":
			in.Kind, in.Ref, in.Body = notifications.Approval, refRun+r.ID, n.approvalMessage(r.ID)
		case "failed", "uncertain":
			in.Kind, in.Level = notifications.Failed, notifications.Error
			in.Body = orDefault(r.Error, "The run stopped before it finished.")
		case "succeeded":
			if r.TriggerType != "manual" {
				break
			}
			in.Kind, in.Level, in.Body = notifications.Finished, notifications.Success, "Run finished"
		}
		if terminalRun(r.Status) {
			delete(n.runs, r.ID)
		}
		if in.Kind == "" {
			return
		}
		in.Body = clipTo(in.Body, noticeBody)
		n.notify(in)
	})
}

func terminalRun(status string) bool {
	return status == "succeeded" || status == "failed" || status == "canceled" || status == "uncertain"
}

func (n *appNotices) approvalMessage(runID string) string {
	if n.d.Automations != nil {
		if list, err := n.d.Automations.Approvals(context.Background()); err == nil {
			for _, a := range list {
				if a.RunID == runID && strings.TrimSpace(a.Message) != "" {
					return a.Message
				}
			}
		}
	}
	return "Waiting for your approval"
}

func (n *appNotices) automationName(id string) string {
	if n.d.Automations != nil {
		if list, err := n.d.Automations.List(context.Background()); err == nil {
			for _, s := range list {
				if s.Definition.ID == id && strings.TrimSpace(s.Definition.Name) != "" {
					return s.Definition.Name
				}
			}
		}
	}
	return "Flow"
}

// --- Permission requests ---

// policyPublisher forwards the policy engine's events and reports its approval queue.
type policyPublisher struct {
	pub     policy.Publisher
	notices *appNotices
}

func (p policyPublisher) Publish(event string, data any) {
	p.pub.Publish(event, data)
	p.notices.policyEvent(event, data)
}

// policyEvent reports each new permission request and resolves it once answered. Requests
// by the user's own actions are answered in the dialog they just opened.
func (n *appNotices) policyEvent(event string, data any) {
	if n == nil {
		return
	}
	m, ok := data.(map[string]any)
	if !ok {
		return
	}
	switch event {
	case "policy.pending":
		items, _ := m["items"].([]policy.Pending)
		n.do(func() {
			for _, p := range items {
				if _, done := n.policy[p.ID]; done || p.Actor == "user" {
					continue
				}
				n.policy[p.ID] = struct{}{}
				n.notify(n.permission(p))
			}
		})
	case "policy.resolved":
		id, _ := m["id"].(string)
		n.do(func() {
			if _, ok := n.policy[id]; ok {
				delete(n.policy, id)
				n.d.resolveNotification(refPolicy + id)
			}
		})
	}
}

func (n *appNotices) permission(p policy.Pending) notifications.Input {
	in := notifications.Input{Kind: notifications.Approval, Ref: refPolicy + p.ID, Source: "policy", Title: "Permission request",
		Body: clipTo(orDefault(firstLine(p.Detail), strings.TrimSpace(p.Action+" "+p.Resource)), noticeBody)}
	actor, id, _ := strings.Cut(p.Actor, ":")
	switch actor {
	case "bot":
		in.Source, in.Title, in.Target = "bot", n.botName(id), "bot:"+id
	case "automation":
		in.Source, in.Title, in.Target = "automation/"+id, n.automationName(id), "automation:"+id
	case "agent":
		in.Source, in.Target = "task", "task:"+id
		if n.d.Agents != nil {
			if s, err := n.d.Agents.Get(id); err == nil {
				in.Title = taskTitle(s)
			}
		}
	}
	return in
}

// --- helpers ---

func orDefault(s, fallback string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return fallback
}

// recentIDs remembers the last ids added, to report a republished snapshot once. It is not
// safe for concurrent use.
type recentIDs struct {
	set  map[string]struct{}
	ring []string
	next int
}

func newRecentIDs(size int) recentIDs {
	return recentIDs{set: map[string]struct{}{}, ring: make([]string, size)}
}

// add records id and reports whether it was new.
func (r *recentIDs) add(id string) bool {
	if _, dup := r.set[id]; dup {
		return false
	}
	if old := r.ring[r.next]; old != "" {
		delete(r.set, old)
	}
	r.ring[r.next] = id
	r.next = (r.next + 1) % len(r.ring)
	r.set[id] = struct{}{}
	return true
}
