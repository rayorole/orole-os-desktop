package daemon

import (
	"context"
	"errors"

	"github.com/rayorole/orole-os-desktop/internal/credentials"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/notifications"
)

// ntfyCredentialStore keeps the ntfy topic and access token; tests replace it with memory.
var ntfyCredentialStore = credentials.New

// NotificationEvent is published on "notifications.changed".
type NotificationEvent struct {
	// Kind: created | updated.
	Kind         string                      `json:"kind"`
	Notification *notifications.Notification `json:"notification,omitempty"`
	Unread       int                         `json:"unread"`
	NeedsYou     int                         `json:"needsYou"`
	// Sound permits the kind's cue only for newly created notifications.
	Sound bool `json:"sound"`
}

// NotificationList is notifications.list's answer.
type NotificationList struct {
	Items    []notifications.Notification `json:"items"`
	Unread   int                          `json:"unread"`
	NeedsYou int                          `json:"needsYou"`
}

// Notify records a notification from daemon code: it applies the kind's preference,
// persists, and publishes notifications.changed. Text derived from external content is
// stored and shown as plain text only. It returns the stored notification (zero ID when
// the kind is off or the approval is already open).
func (d *Daemon) Notify(in notifications.Input) (notifications.Notification, error) {
	res, err := d.notifyResult(context.Background(), in)
	if err != nil || !res.Stored {
		return notifications.Notification{}, err
	}
	return res.Notification, nil
}

func (d *Daemon) notifyResult(ctx context.Context, in notifications.Input) (notifications.Result, error) {
	st := notifications.New(d.DB.DB)
	res, err := st.Notify(ctx, in)
	if err != nil {
		return res, err
	}
	// Push is independent of the in-app toggle and runs in the background: it never delays or fails the notification.
	if res.Push && d.Ntfy != nil {
		d.Ntfy.Push(res.Notification)
	}
	if !res.Stored {
		return res, nil
	}
	c, _ := st.Counts(ctx)
	n := res.Notification
	d.IPC.Publish("notifications.changed", NotificationEvent{Kind: "created", Notification: &n, Unread: c.Unread, NeedsYou: c.NeedsYou, Sound: res.Sound})
	d.jarvisNotification(n)
	return res, nil
}

// resolveNotification ends the open approval with ref, once the wait it reports is over.
func (d *Daemon) resolveNotification(ref string) {
	st := notifications.New(d.DB.DB)
	ctx := context.Background()
	if n, err := st.Resolve(ctx, ref); err != nil {
		d.Log.Warn("resolve notification failed", "ref", ref, "err", err)
	} else if n > 0 {
		d.notificationsUpdated(ctx, st)
	}
}

func (d *Daemon) notificationsUpdated(ctx context.Context, st *notifications.Store) {
	c, _ := st.Counts(ctx)
	d.IPC.Publish("notifications.changed", NotificationEvent{Kind: "updated", Unread: c.Unread, NeedsYou: c.NeedsYou})
}

func mapNotifErr(err error) error {
	if errors.Is(err, notifications.ErrInvalid) {
		return &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
	}
	return err
}

func (d *Daemon) registerNotifications() {
	st := notifications.New(d.DB.DB)
	d.Ntfy = notifications.NewNtfy(d.DB.DB, ntfyCredentialStore(), d.Log)

	handle(d, "notifications.list", func(ctx context.Context, p notifications.ListOptions) (NotificationList, error) {
		items, err := st.List(ctx, p)
		if err != nil {
			return NotificationList{}, err
		}
		c, err := st.Counts(ctx)
		return NotificationList{Items: items, Unread: c.Unread, NeedsYou: c.NeedsYou}, err
	})
	handle(d, "notifications.notify", func(ctx context.Context, p notifications.Input) (notifications.Notification, error) {
		res, err := d.notifyResult(ctx, p)
		if err != nil || !res.Stored {
			return notifications.Notification{}, mapNotifErr(err)
		}
		return res.Notification, nil
	})
	handle(d, "notifications.test", func(ctx context.Context, _ none) (notifications.Result, error) {
		res, err := d.notifyResult(ctx, notifications.Input{Source: "system.test", Kind: notifications.Message, Title: "Test notification", Body: "Notifications and sounds are working."})
		return res, mapNotifErr(err)
	})
	handle(d, "notifications.markRead", func(ctx context.Context, p struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}) (int64, error) {
		n, err := st.MarkRead(ctx, p.IDs, p.All)
		if err == nil && n > 0 {
			d.notificationsUpdated(ctx, st)
		}
		return n, mapNotifErr(err)
	})
	handle(d, "notifications.prefs.get", func(ctx context.Context, _ none) (notifications.Prefs, error) {
		return st.Prefs(ctx)
	})
	handle(d, "notifications.prefs.set", func(ctx context.Context, p notifications.Prefs) (notifications.Prefs, error) {
		stored, err := st.SetPrefs(ctx, p)
		if err != nil {
			return p, mapNotifErr(err)
		}
		d.IPC.Publish("settings.changed", map[string]any{"key": notifications.PrefsKey, "value": stored})
		return stored, nil
	})
	handle(d, "notifications.ntfy.get", func(ctx context.Context, _ none) (notifications.NtfyView, error) {
		return d.Ntfy.Config(ctx)
	})
	handle(d, "notifications.ntfy.set", func(ctx context.Context, p notifications.NtfyUpdate) (notifications.NtfyView, error) {
		v, err := d.Ntfy.Update(ctx, p)
		return v, mapNotifErr(err)
	})
	handle(d, "notifications.ntfy.test", func(ctx context.Context, _ none) (notifications.NtfyTestResult, error) {
		if err := d.Ntfy.SendTest(ctx); err != nil {
			d.Log.Warn("test push not sent", "err", err)
			return notifications.NtfyTestResult{Error: err.Error()}, nil
		}
		return notifications.NtfyTestResult{OK: true}, nil
	})
}
