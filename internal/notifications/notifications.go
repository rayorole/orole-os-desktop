// Package notifications persists the notification center: what happened, from
// which source, what it is about, and whether the user has seen it. It is
// stateless over the daemon database; rendering and sounds belong to the desktop.
//
// Notification text can originate from untrusted content (mail subjects, issue
// titles, MCP results, bot replies); consumers must render it as plain text.
package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Level is a notification's severity.
type Level string

// Levels.
const (
	Info    Level = "info"
	Success Level = "success"
	Warning Level = "warning"
	Error   Level = "error"
)

// Kind is the event a notification reports. Preferences and sounds are per kind.
type Kind string

// Kinds.
const (
	// Approval: a task, bot or flow waits for the user's decision or answer. It stays in
	// "Needs you" until its producer resolves its ref.
	Approval Kind = "approval"
	// Failed: a task, bot reply or flow run stopped with an error.
	Failed Kind = "failed"
	// Finished: a task or a flow run the user started is done.
	Finished Kind = "finished"
	// Reply: a bot answered.
	Reply Kind = "reply"
	// Message: an explicit notification, such as a flow's Notify step.
	Message Kind = "message"
)

// Kinds lists every kind in display order.
var Kinds = []Kind{Approval, Failed, Finished, Reply, Message}

func (k Kind) valid() bool {
	switch k {
	case Approval, Failed, Finished, Reply, Message:
		return true
	}
	return false
}

// Limits.
const (
	MaxTitle    = 200
	MaxBody     = 2000
	MaxSource   = 64
	MaxRef      = 200
	MaxKeep     = 1000 // rows kept; read rows that need nothing go first
	MaxList     = 500
	DefaultList = 100
	// PrefsKey is the settings key holding Prefs.
	PrefsKey = "notifications.prefs"
)

// ErrInvalid marks rejected input.
var ErrInvalid = errors.New("invalid notification")

var sourceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*$`)

// Notification is one stored entry.
type Notification struct {
	ID     string `json:"id"`
	At     int64  `json:"at"` // unix seconds
	Source string `json:"source"`
	Kind   Kind   `json:"kind"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Level  Level  `json:"level"`
	// Target is what the notification is about: "task:<id>", "bot:<id>", "automation:<id>" or "".
	Target string `json:"target"`
	Read   bool   `json:"read"`
	// NeedsYou is true while the approval this reports is still waiting.
	NeedsYou bool `json:"needsYou"`
}

// Input is a notification to record.
type Input struct {
	Source string `json:"source"`
	Kind   Kind   `json:"kind"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Level  Level  `json:"level"`
	Target string `json:"target"`
	// Ref identifies the wait an Approval reports; required for approvals, ignored otherwise.
	// While an approval with the same ref is open, another is not recorded.
	Ref string `json:"ref"`
}

// KindPref is the preference for one kind.
type KindPref struct {
	// InApp false drops the kind's notifications. Approvals are always shown.
	InApp bool `json:"inApp"`
	// Sound plays the kind's cue when one arrives.
	Sound bool `json:"sound"`
	// Push also sends the kind's notifications to ntfy while push is switched on (see Ntfy).
	// Off by default and independent of InApp. Preferences saved before it existed decode to off.
	Push bool `json:"push"`
}

// Prefs are per-kind preferences. Missing kinds use defaults.
type Prefs struct {
	Kinds map[Kind]KindPref `json:"kinds"`
}

// DefaultPrefs: every kind shown and audible, none pushed.
func DefaultPrefs() Prefs {
	p := Prefs{Kinds: map[Kind]KindPref{}}
	for _, k := range Kinds {
		p.Kinds[k] = KindPref{InApp: true, Sound: true}
	}
	return p
}

// normalize fills missing kinds with defaults, drops unknown ones and keeps approvals visible:
// a hidden approval would leave something waiting on the user with no sign of it.
func (p Prefs) normalize() Prefs {
	out := DefaultPrefs()
	for k, v := range p.Kinds {
		if k.valid() {
			out.Kinds[k] = v
		}
	}
	a := out.Kinds[Approval]
	a.InApp = true
	out.Kinds[Approval] = a
	return out
}

// For returns the effective preference of a kind.
func (p Prefs) For(kind Kind) KindPref {
	v, ok := p.Kinds[kind]
	if !ok {
		v = KindPref{InApp: true, Sound: true}
	}
	if kind == Approval {
		v.InApp = true
	}
	return v
}

// Store is the notification table.
type Store struct{ DB *sql.DB }

// New returns a Store over db.
func New(db *sql.DB) *Store { return &Store{DB: db} }

// validate normalises and checks an input notification.
func validate(in Input) (Input, error) {
	if !sourceRe.MatchString(in.Source) || len(in.Source) > MaxSource {
		return in, fmt.Errorf("%w: source must be lower-case letters, digits and . _ : / - (max %d)", ErrInvalid, MaxSource)
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || utf8.RuneCountInString(in.Title) > MaxTitle {
		return in, fmt.Errorf("%w: title is required (max %d characters)", ErrInvalid, MaxTitle)
	}
	if utf8.RuneCountInString(in.Body) > MaxBody {
		return in, fmt.Errorf("%w: body too long (max %d characters)", ErrInvalid, MaxBody)
	}
	switch in.Level {
	case "":
		in.Level = Info
	case Info, Success, Warning, Error:
	default:
		return in, fmt.Errorf("%w: unknown level %q", ErrInvalid, in.Level)
	}
	if in.Kind == "" {
		in.Kind = Message
	}
	if !in.Kind.valid() {
		return in, fmt.Errorf("%w: unknown kind %q", ErrInvalid, in.Kind)
	}
	if len(in.Target) > MaxRef || len(in.Ref) > MaxRef {
		return in, fmt.Errorf("%w: target and ref are limited to %d bytes", ErrInvalid, MaxRef)
	}
	if in.Kind != Approval {
		in.Ref = ""
	} else if in.Ref == "" {
		return in, fmt.Errorf("%w: an approval needs a ref so it can be resolved", ErrInvalid)
	}
	return in, nil
}

// Prefs loads preferences from the settings table. Values saved in an older shape decode
// to defaults.
func (s *Store) Prefs(ctx context.Context) (Prefs, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, PrefsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultPrefs(), nil
	}
	if err != nil {
		return DefaultPrefs(), err
	}
	var p Prefs
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return DefaultPrefs(), fmt.Errorf("decode notification preferences: %w", err)
	}
	return p.normalize(), nil
}

// SetPrefs stores preferences and returns what was stored.
func (s *Store) SetPrefs(ctx context.Context, p Prefs) (Prefs, error) {
	for k := range p.Kinds {
		if !k.valid() {
			return Prefs{}, fmt.Errorf("%w: unknown kind %q", ErrInvalid, k)
		}
	}
	p = p.normalize()
	b, err := json.Marshal(p)
	if err != nil {
		return Prefs{}, err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		PrefsKey, string(b), time.Now().Unix())
	return p, err
}

// Result of Notify.
type Result struct {
	Notification Notification `json:"notification"`
	// Stored is false when the kind is turned off, or an approval with the same ref is
	// already open.
	Stored bool `json:"stored"`
	// Sound applies only to this newly created notification, never history.
	Sound bool `json:"sound"`
	// Push is true when the kind's Push toggle is on and the notification is new, even when
	// the kind is hidden in app. The caller sends it to ntfy.
	Push bool `json:"push"`
}

// Notify validates, applies the kind's preference, and persists.
func (s *Store) Notify(ctx context.Context, in Input) (Result, error) {
	in, err := validate(in)
	if err != nil {
		return Result{}, err
	}
	prefs, err := s.Prefs(ctx)
	if err != nil {
		return Result{}, err
	}
	pref := prefs.For(in.Kind)
	needsYou := in.Kind == Approval
	n := Notification{ID: uuid.NewString(), At: time.Now().Unix(), Source: in.Source, Kind: in.Kind, Title: in.Title, Body: in.Body, Level: in.Level, Target: in.Target, NeedsYou: needsYou}
	if !pref.InApp {
		return Result{Notification: n, Push: pref.Push}, nil
	}
	// One open approval per ref: a producer that re-reports the same wait (a restart, a
	// repeated status) does not stack duplicates.
	res, err := s.DB.ExecContext(ctx, `INSERT INTO notifications(id,at,source,kind,title,body,level,target,ref,needs_you)
		SELECT ?,?,?,?,?,?,?,?,?,? WHERE ?='' OR NOT EXISTS (SELECT 1 FROM notifications WHERE needs_you=1 AND ref=?)`,
		n.ID, n.At, n.Source, string(n.Kind), n.Title, n.Body, string(n.Level), n.Target, in.Ref, needsYou, in.Ref, in.Ref)
	if err != nil {
		return Result{}, err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return Result{Notification: n}, nil
	}
	s.prune(ctx)
	return Result{Notification: n, Stored: true, Sound: pref.Sound, Push: pref.Push}, nil
}

// Resolve ends the open approvals with ref: the wait they reported is over.
func (s *Store) Resolve(ctx context.Context, ref string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE notifications SET needs_you=0 WHERE needs_you=1 AND ref=?`, ref)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// OpenRefs lists the refs of approvals still marked as waiting.
func (s *Store) OpenRefs(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT ref FROM notifications WHERE needs_you=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func (s *Store) prune(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM notifications WHERE id IN (
		SELECT id FROM notifications ORDER BY needs_you ASC, (read=1 OR dismissed=1) DESC, at ASC LIMIT max(0, (SELECT COUNT(*) FROM notifications) - ?))`, MaxKeep)
}

// ListOptions filters List.
type ListOptions struct {
	// NeedsYou lists only approvals that are still waiting.
	NeedsYou bool `json:"needsYou,omitempty"`
	Limit    int  `json:"limit,omitempty"`
}

// List returns newest first. Rows dismissed before dismissal was removed stay hidden.
func (s *Store) List(ctx context.Context, o ListOptions) ([]Notification, error) {
	q := `SELECT id,at,source,kind,title,body,level,target,read,needs_you FROM notifications WHERE dismissed=0`
	if o.NeedsYou {
		q += ` AND needs_you=1`
	}
	limit := o.Limit
	if limit <= 0 {
		limit = DefaultList
	}
	if limit > MaxList {
		limit = MaxList
	}
	q += ` ORDER BY at DESC, rowid DESC LIMIT ?`
	rows, err := s.DB.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var kind, lvl string
		var r, w int
		if err := rows.Scan(&n.ID, &n.At, &n.Source, &kind, &n.Title, &n.Body, &lvl, &n.Target, &r, &w); err != nil {
			return nil, err
		}
		n.Kind, n.Level, n.Read, n.NeedsYou = Kind(kind), Level(lvl), r != 0, w != 0
		out = append(out, n)
	}
	return out, rows.Err()
}

// Counts are the badge numbers.
type Counts struct {
	Unread   int `json:"unread"`
	NeedsYou int `json:"needsYou"`
}

// Counts counts visible unread notifications and open approvals.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(read=0),0), COALESCE(SUM(needs_you=1),0) FROM notifications WHERE dismissed=0`).Scan(&c.Unread, &c.NeedsYou)
	return c, err
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// MarkRead marks ids, or every notification when all is set, read.
func (s *Store) MarkRead(ctx context.Context, ids []string, all bool) (int64, error) {
	var res sql.Result
	var err error
	switch {
	case all:
		res, err = s.DB.ExecContext(ctx, `UPDATE notifications SET read=1 WHERE read=0`)
	case len(ids) > 0:
		if len(ids) > 1000 {
			return 0, fmt.Errorf("%w: too many ids", ErrInvalid)
		}
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		res, err = s.DB.ExecContext(ctx, `UPDATE notifications SET read=1 WHERE read=0 AND id IN (`+placeholders(len(ids))+`)`, args...)
	default:
		return 0, fmt.Errorf("%w: give ids or all", ErrInvalid)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
