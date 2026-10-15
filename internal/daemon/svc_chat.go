package daemon

import (
	"context"
	"errors"

	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
)

// chatError maps chat errors to IPC errors. Messages from chat are written for the user.
func chatError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, chat.ErrInvalid):
		return &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
	case errors.Is(err, chat.ErrNotFound):
		return &ipc.Error{Code: ipc.CodeNotFound, Message: err.Error()}
	case errors.Is(err, chat.ErrBusy):
		return &ipc.Error{Code: "conflict", Message: err.Error()}
	case errors.Is(err, chat.ErrClosed), errors.Is(err, context.Canceled):
		return &ipc.Error{Code: ipc.CodeCanceled, Message: err.Error()}
	}
	return err
}

// registerChat runs after registerLLM. The Manager is created without memory or tools; the later
// registrars (registerChatMemory, registerChatTools) attach them with SetMemory / AddTools.
// Memory, knowledge, skill and tool-info IPC methods are registered by those registrars.
func (d *Daemon) registerChat() {
	replies := newBotReplies(d.Events)
	m, err := chat.New(chat.Config{
		DB: d.DB.DB, DataDir: d.DataDir, LLM: d.LLM, Log: d.Log,
		Publish: func(event string, payload any) {
			d.IPC.Publish(event, payload)
			replies.observe(event, payload)
			d.notices.chatMessage(event, payload)
		},
	})
	if err != nil {
		d.Log.Error("chat service unavailable", "err", err)
		return
	}
	d.Chat = m
	if d.Policy != nil {
		d.Policy.SetAutoApprove(d.botAutoApproveHook(m))
	}
	if d.Automations != nil {
		d.Automations.SetBotMessenger(botAutomation{d: d})
	}

	handle(d, "chat.bots.list", func(ctx context.Context, _ none) ([]chat.Bot, error) {
		out, err := m.Bots(ctx)
		return out, chatError(err)
	})
	handle(d, "chat.bots.get", func(ctx context.Context, p chat.BotRef) (chat.Bot, error) {
		out, err := m.Bot(ctx, p.ID)
		return out, chatError(err)
	})
	handle(d, "chat.bots.save", func(ctx context.Context, p chat.BotInput) (chat.Bot, error) {
		create := p.ID == ""
		out, err := m.SaveBot(ctx, p)
		if err == nil && create {
			d.audit("chat.bot.create", out.ID, "allowed", "Bot created")
		}
		return out, chatError(err)
	})
	handle(d, "chat.bots.archive", func(ctx context.Context, p chat.BotRef) (none, error) {
		err := m.ArchiveBot(ctx, p.ID)
		if err == nil {
			d.audit("chat.bot.delete", p.ID, "allowed", "Bot archived")
			d.deleteBotComputer(p.ID)
		}
		return none{}, chatError(err)
	})
	handle(d, "chat.bots.export", func(ctx context.Context, p chat.BotRef) (chat.BotExport, error) {
		out, err := m.ExportBot(ctx, p.ID)
		return out, chatError(err)
	})
	handle(d, "chat.bots.import", func(ctx context.Context, p chat.BotExport) (chat.Bot, error) {
		out, err := m.ImportBot(ctx, p)
		if err == nil {
			d.audit("chat.bot.create", out.ID, "allowed", "Bot imported")
		}
		return out, chatError(err)
	})
	handle(d, "chat.messages.list", func(ctx context.Context, p chat.MessagesRequest) (chat.MessagePage, error) {
		out, err := m.Messages(ctx, p)
		return out, chatError(err)
	})
	handle(d, "chat.send", func(ctx context.Context, p chat.SendRequest) (chat.Message, error) {
		out, err := m.Send(ctx, p)
		return out, chatError(err)
	})
	handle(d, "chat.bots.pin", func(ctx context.Context, p chat.PinRequest) (none, error) {
		return none{}, chatError(m.PinBot(ctx, p.ID, p.Pinned))
	})
	handle(d, "chat.cancel", func(ctx context.Context, p chat.BotRef) (none, error) {
		return none{}, chatError(m.Cancel(ctx, p.ID))
	})
	handle(d, "chat.queue.pause", func(ctx context.Context, p chat.BotRef) (none, error) {
		return none{}, chatError(m.PauseQueue(ctx, p.ID))
	})
	handle(d, "chat.queue.resume", func(ctx context.Context, p chat.BotRef) (none, error) {
		return none{}, chatError(m.ResumeQueue(ctx, p.ID))
	})
	handle(d, "chat.queue.cancel", func(ctx context.Context, p chat.QueueItemRef) (none, error) {
		return none{}, chatError(m.CancelQueued(ctx, p.BotID, p.MessageID))
	})
	handle(d, "chat.regenerate", func(ctx context.Context, p chat.RegenerateRequest) (none, error) {
		return none{}, chatError(m.Regenerate(ctx, p))
	})
	handle(d, "chat.clear", func(ctx context.Context, p chat.BotRef) (none, error) {
		err := m.Clear(ctx, p.ID)
		if err == nil {
			d.audit("chat.bot.clear", p.ID, "allowed", "Conversation cleared")
		}
		return none{}, chatError(err)
	})
	handle(d, "chat.draft.send", func(ctx context.Context, p chat.DraftRequest) (chat.Message, error) {
		out, err := m.DraftSend(ctx, p)
		return out, chatError(err)
	})
	handle(d, "chat.attachments.begin", func(ctx context.Context, p chat.AttachmentBegin) (chat.AttachmentUpload, error) {
		out, err := m.BeginAttachment(p)
		return out, chatError(err)
	})
	handle(d, "chat.attachments.chunk", func(ctx context.Context, p chat.AttachmentChunk) (none, error) {
		return none{}, chatError(m.AttachmentChunk(p))
	})
	handle(d, "chat.attachments.commit", func(ctx context.Context, p chat.AttachmentCommit) (chat.Attachment, error) {
		out, err := m.CommitAttachment(ctx, p)
		return out, chatError(err)
	})
	handle(d, "chat.attachments.discard", func(ctx context.Context, p chat.AttachmentRef) (none, error) {
		return none{}, chatError(m.DiscardAttachment(ctx, p.ID))
	})
	handle(d, "chat.attachments.read", func(ctx context.Context, p chat.AttachmentRef) (chat.AttachmentData, error) {
		out, err := m.ReadAttachment(ctx, p.ID)
		return out, chatError(err)
	})
}
