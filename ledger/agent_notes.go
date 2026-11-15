package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Continuity: what an agent asked and was told, what it said it would do,
// and what it remembers about the person. Every row is one the person can
// see and change; none holds money facts.

var noteMethods = map[string]bool{"ask_question": true, "withdraw_question": true, "open_loop": true, "close_loop": true, "update_agent_profile": true}

// NoteMethod reports a write any of the Book's agents may make directly,
// proposing ones included: notes, never the books.
func NoteMethod(method string) bool { return noteMethods[method] }

func noteRead(method string) bool {
	return method == "list_agent_questions" || method == "list_agent_loops" || method == "get_agent_profile"
}

// AgentQuestion is a question an agent asked the owner, once per subject.
type AgentQuestion struct {
	ID          string          `json:"id"`
	SubjectKind string          `json:"subject_kind"`
	SubjectKey  string          `json:"subject_key"`
	Question    string          `json:"question"`
	Status      string          `json:"status"`
	Answer      json.RawMessage `json:"answer,omitempty"`
	AskedAt     time.Time       `json:"asked_at"`
	AnsweredAt  *time.Time      `json:"answered_at,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	FromOwner   bool            `json:"from_owner,omitempty"`
	// Choices are what the owner picks from, for a question about which
	// Book a charge belongs to (entity) or which costs a payment repays
	// (settlement). The answer is {"value": one of them}.
	Choices json.RawMessage `json:"choices,omitempty"`
}

// QuestionChoice is one answer the owner can pick.
type QuestionChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type AskQuestion struct {
	SubjectKind string           `json:"subject_kind"`
	SubjectKey  string           `json:"subject_key"`
	Question    string           `json:"question"`
	Choices     []QuestionChoice `json:"choices,omitempty"`
}

type WithdrawQuestion struct {
	QuestionID string `json:"question_id"`
	Reason     string `json:"reason"`
}

type QuestionQuery struct {
	Status      string `json:"status,omitempty"`
	SubjectKind string `json:"subject_kind,omitempty"`
}

// AgentLoop is a loose end: something an agent said it would do, or was
// asked to remind the person of.
type AgentLoop struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	SubjectKind  string     `json:"subject_kind,omitempty"`
	SubjectKey   string     `json:"subject_key,omitempty"`
	Text         string     `json:"text"`
	DueOn        string     `json:"due_on,omitempty"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	ClosedReason string     `json:"closed_reason,omitempty"`
}

type OpenLoop struct {
	RequestKey  string `json:"request_key"`
	Kind        string `json:"kind"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectKey  string `json:"subject_key,omitempty"`
	Text        string `json:"text"`
	DueOn       string `json:"due_on,omitempty"`
}

type CloseLoop struct {
	LoopID string `json:"loop_id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type LoopQuery struct {
	Status    string `json:"status,omitempty"`
	DueBefore string `json:"due_before,omitempty"`
}

// AgentProfile is what agents remember about the person: tone and
// preferences, never money, and never instructions that change the books
// (those are rules).
type AgentProfile struct {
	Goal         string          `json:"goal"`
	Preferences  json.RawMessage `json:"preferences"`
	Instructions string          `json:"instructions"`
	Revision     int64           `json:"revision"`
	Reason       string          `json:"reason,omitempty"`
	UpdatedAt    *time.Time      `json:"updated_at,omitempty"`
}

type UpdateAgentProfile struct {
	ExpectedRevision int64           `json:"expected_revision"`
	Goal             *string         `json:"goal,omitempty"`
	Preferences      json.RawMessage `json:"preferences,omitempty"`
	Instructions     *string         `json:"instructions,omitempty"`
	Reason           string          `json:"reason"`
}

const questionColumns = `q.id::text,q.subject_kind,q.subject_key,q.question,q.status,q.answer,q.asked_at,q.answered_at,q.reason,q.connection_id IS NULL,q.choices`

func scanQuestion(row pgx.Row) (AgentQuestion, error) {
	var v AgentQuestion
	var answer, choices []byte
	err := row.Scan(&v.ID, &v.SubjectKind, &v.SubjectKey, &v.Question, &v.Status, &answer, &v.AskedAt, &v.AnsweredAt, &v.Reason, &v.FromOwner, &choices)
	if len(answer) > 0 {
		v.Answer = answer
	}
	if len(choices) > 0 {
		v.Choices = choices
	}
	return v, err
}

func readQuestions(ctx context.Context, tx pgx.Tx, s Scope, q QuestionQuery) ([]AgentQuestion, error) {
	rows, err := tx.Query(ctx, `SELECT `+questionColumns+` FROM public.agent_questions q WHERE q.tenant_id=$1 AND q.book_id=$2
 AND ($3='' AND q.status IN ('open','answered') OR q.status=$3) AND ($4='' OR q.subject_kind=$4) ORDER BY q.asked_at DESC LIMIT 200`, s.TenantID, s.BookID, q.Status, q.SubjectKind)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []AgentQuestion{}
	for rows.Next() {
		v, err := scanQuestion(rows)
		if err != nil {
			return nil, dbError(err)
		}
		out = append(out, v)
	}
	return out, dbError(rows.Err())
}

func readLoops(ctx context.Context, tx pgx.Tx, s Scope, q LoopQuery) ([]AgentLoop, error) {
	if q.Status == "" {
		q.Status = "open"
	}
	var due any
	if q.DueBefore != "" {
		due = q.DueBefore
	}
	rows, err := tx.Query(ctx, `SELECT id::text,kind,coalesce(subject_kind,''),coalesce(subject_key,''),text,coalesce(due_on::text,''),status,created_at,closed_at,closed_reason
 FROM public.agent_loops WHERE tenant_id=$1 AND book_id=$2 AND ($3='all' OR status=$3) AND ($4::date IS NULL OR due_on<$4::date)
 ORDER BY due_on NULLS LAST,created_at LIMIT 200`, s.TenantID, s.BookID, q.Status, due)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []AgentLoop{}
	for rows.Next() {
		var v AgentLoop
		if err := rows.Scan(&v.ID, &v.Kind, &v.SubjectKind, &v.SubjectKey, &v.Text, &v.DueOn, &v.Status, &v.CreatedAt, &v.ClosedAt, &v.ClosedReason); err != nil {
			return nil, dbError(err)
		}
		out = append(out, v)
	}
	return out, dbError(rows.Err())
}

func readProfile(ctx context.Context, tx pgx.Tx, s Scope) (AgentProfile, error) {
	v := AgentProfile{Preferences: json.RawMessage(`{}`)}
	var prefs []byte
	err := tx.QueryRow(ctx, "SELECT goal,preferences,instructions,revision,reason,updated_at FROM public.agent_profile WHERE tenant_id=$1 AND book_id=$2", s.TenantID, s.BookID).
		Scan(&v.Goal, &prefs, &v.Instructions, &v.Revision, &v.Reason, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if len(prefs) > 0 {
		v.Preferences = prefs
	}
	return v, dbError(err)
}

func noteError(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "VP020" {
		return &RefusedError{Reason: p.Message}
	}
	// A length or shape the table refuses is said in plain words, never in
	// the database's.
	if errors.As(err, &p) && p.Code == "23514" && strings.HasPrefix(p.Message, "new row for relation") {
		return &RefusedError{Reason: "That's longer than these notes hold, or not the shape they take."}
	}
	if errors.As(err, &p) && (p.Code == "23514" || p.Code == "42501") && !strings.HasPrefix(p.Message, "new row violates") {
		msg := p.Message
		return &RefusedError{Reason: strings.ToUpper(msg[:1]) + msg[1:] + "."}
	}
	return dbError(err)
}

// noteCall reads and writes notes for an agent connection or the owner.
func noteCall(ctx context.Context, tx pgx.Tx, s Scope, method string, input []byte) (json.RawMessage, error) {
	switch method {
	case "list_agent_questions":
		var q QuestionQuery
		if strictjson.Decode(input, &q) != nil {
			return nil, ErrInvalid
		}
		out, err := readQuestions(ctx, tx, s, q)
		return raw(struct {
			Questions []AgentQuestion `json:"questions"`
		}{out}), err
	case "list_agent_loops":
		var q LoopQuery
		if strictjson.Decode(input, &q) != nil || q.DueBefore != "" && !ValidDate(q.DueBefore) {
			return nil, ErrInvalid
		}
		out, err := readLoops(ctx, tx, s, q)
		return raw(struct {
			Loops []AgentLoop `json:"loops"`
		}{out}), err
	case "get_agent_profile":
		if strictjson.Decode(input, &struct{}{}) != nil {
			return nil, ErrInvalid
		}
		out, err := readProfile(ctx, tx, s)
		return raw(out), err
	case "ask_question":
		var q AskQuestion
		if strictjson.Decode(input, &q) != nil || strings.TrimSpace(q.Question) == "" {
			return nil, ErrInvalid
		}
		// Never ask twice: an open or answered question on the subject
		// stands, and the agent gets it back instead.
		existing, err := scanQuestion(tx.QueryRow(ctx, `SELECT `+questionColumns+` FROM public.agent_questions q WHERE q.tenant_id=$1 AND q.book_id=$2
 AND q.subject_kind=$3 AND q.subject_key=$4 AND q.status IN ('open','answered')`, s.TenantID, s.BookID, q.SubjectKind, q.SubjectKey))
		if err == nil {
			if existing.Status == "answered" {
				return nil, &RefusedError{Reason: "Already answered on " + existing.AnsweredAt.Format(time.DateOnly) + ": " + string(existing.Answer) + ". Use that answer; don't ask again."}
			}
			return nil, &RefusedError{Reason: "Already asked on " + existing.AskedAt.Format(time.DateOnly) + " and waiting for the owner (question " + existing.ID + "). Don't ask again."}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, dbError(err)
		}
		if s.ConnectionID != "" {
			var open int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM public.agent_questions WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND status='open'",
				s.TenantID, s.BookID, s.ActorID).Scan(&open); err != nil {
				return nil, dbError(err)
			}
			if open >= maxOpenQuestions {
				return nil, &RefusedError{Reason: fmt.Sprintf("%d of your questions are already waiting for the owner. Wait for answers, or withdraw one, before asking more.", open)}
			}
		}
		id := ID()
		var choices *string
		if q.Choices != nil {
			c := string(raw(q.Choices))
			choices = &c
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.agent_questions(tenant_id,book_id,id,actor_id,subject_kind,subject_key,question,choices) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb)`,
			s.TenantID, s.BookID, id, s.ActorID, q.SubjectKind, q.SubjectKey, strings.TrimSpace(q.Question), choices); err != nil {
			return nil, noteError(err)
		}
		v, err := scanQuestion(tx.QueryRow(ctx, `SELECT `+questionColumns+` FROM public.agent_questions q WHERE q.tenant_id=$1 AND q.book_id=$2 AND q.id=$3`, s.TenantID, s.BookID, id))
		return raw(v), dbError(err)
	case "withdraw_question":
		var q WithdrawQuestion
		if strictjson.Decode(input, &q) != nil || !ValidID(q.QuestionID) || strings.TrimSpace(q.Reason) == "" {
			return nil, ErrInvalid
		}
		tag, err := tx.Exec(ctx, "UPDATE public.agent_questions SET status='withdrawn',reason=$4 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND status='open'",
			s.TenantID, s.BookID, q.QuestionID, clip(q.Reason, 1000))
		if err != nil {
			return nil, noteError(err)
		}
		if tag.RowsAffected() == 0 {
			return nil, ErrNotFound
		}
		return raw(map[string]string{"question_id": q.QuestionID, "status": "withdrawn"}), nil
	case "open_loop":
		var q OpenLoop
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || q.DueOn != "" && !ValidDate(q.DueOn) || (q.SubjectKind == "") != (q.SubjectKey == "") {
			return nil, ErrInvalid
		}
		var subjectKind, subjectKey, due any
		if q.SubjectKind != "" {
			subjectKind, subjectKey = q.SubjectKind, q.SubjectKey
		}
		if q.DueOn != "" {
			due = q.DueOn
		}
		// A retry with the same request key returns the loop it opened.
		var id string
		err := tx.QueryRow(ctx, "SELECT id::text FROM public.agent_loops WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4",
			s.TenantID, s.BookID, s.ActorID, q.RequestKey).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			id = ID()
			_, err = tx.Exec(ctx, `INSERT INTO public.agent_loops(tenant_id,book_id,id,actor_id,request_key,kind,subject_kind,subject_key,text,due_on)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::date)`, s.TenantID, s.BookID, id, s.ActorID, q.RequestKey, q.Kind, subjectKind, subjectKey, strings.TrimSpace(q.Text), due)
		}
		if err != nil {
			return nil, noteError(err)
		}
		loops, err := readLoops(ctx, tx, s, LoopQuery{Status: "all"})
		for _, l := range loops {
			if l.ID == id {
				return raw(l), err
			}
		}
		return raw(map[string]string{"id": id}), err
	case "close_loop":
		var q CloseLoop
		if strictjson.Decode(input, &q) != nil || !ValidID(q.LoopID) || q.Status != "done" && q.Status != "dropped" || strings.TrimSpace(q.Reason) == "" {
			return nil, ErrInvalid
		}
		// A loose end is closed by the agent that opened it, or by the owner.
		tag, err := tx.Exec(ctx, "UPDATE public.agent_loops SET status=$4,closed_reason=$5,closed_at=clock_timestamp() WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND status='open' AND ($6 OR actor_id=$7)",
			s.TenantID, s.BookID, q.LoopID, q.Status, clip(q.Reason, 1000), s.ConnectionID == "", s.ActorID)
		if err != nil {
			return nil, noteError(err)
		}
		if tag.RowsAffected() == 0 {
			return nil, ErrNotFound
		}
		return raw(map[string]string{"loop_id": q.LoopID, "status": q.Status}), nil
	case "update_agent_profile":
		var q UpdateAgentProfile
		if strictjson.Decode(input, &q) != nil || q.ExpectedRevision < 0 || strings.TrimSpace(q.Reason) == "" {
			return nil, ErrInvalid
		}
		return writeProfile(ctx, tx, s, q)
	}
	return nil, ErrDenied
}

func writeProfile(ctx context.Context, tx pgx.Tx, s Scope, q UpdateAgentProfile) (json.RawMessage, error) {
	current, err := readProfile(ctx, tx, s)
	if err != nil {
		return nil, err
	}
	if current.Revision != q.ExpectedRevision {
		return nil, &RefusedError{Reason: "The profile changed since you read it; read it again (get_agent_profile) and keep what the person set."}
	}
	next := current
	if q.Goal != nil {
		next.Goal = strings.TrimSpace(*q.Goal)
	}
	if len(q.Preferences) > 0 {
		var object map[string]any
		if json.Unmarshal(q.Preferences, &object) != nil {
			return nil, &RefusedError{Reason: "preferences is a JSON object, like {\"tone\":\"plain\"}."}
		}
		next.Preferences = q.Preferences
	}
	if q.Instructions != nil {
		next.Instructions = strings.TrimSpace(*q.Instructions)
	}
	// An agent adds to the notes. What is already there, whoever wrote it,
	// is changed or removed only by the owner, so one agent can't rewrite
	// what the owner said or what another agent relies on.
	if s.ConnectionID != "" && !keepsNotes(current, next) {
		return nil, &RefusedError{Reason: "These notes already hold that, and an agent can add to them but not change or remove what's there. Keep every existing line and value and add yours; the owner changes the rest in Vult."}
	}
	if q.ExpectedRevision == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO public.agent_profile(tenant_id,book_id,goal,preferences,instructions,revision,actor_id,reason) VALUES($1,$2,$3,$4::jsonb,$5,1,$6,$7)`,
			s.TenantID, s.BookID, next.Goal, string(next.Preferences), next.Instructions, s.ActorID, clip(q.Reason, 1000))
	} else {
		_, err = tx.Exec(ctx, `UPDATE public.agent_profile SET goal=$3,preferences=$4::jsonb,instructions=$5,revision=revision+1,actor_id=$6,reason=$7 WHERE tenant_id=$1 AND book_id=$2`,
			s.TenantID, s.BookID, next.Goal, string(next.Preferences), next.Instructions, s.ActorID, clip(q.Reason, 1000))
	}
	if err != nil {
		return nil, noteError(err)
	}
	out, err := readProfile(ctx, tx, s)
	return raw(out), err
}

// keepsNotes reports whether next still holds everything current does: the
// goal if one is set, every line of the instructions, and every value in
// the preferences that isn't empty.
func keepsNotes(current, next AgentProfile) bool {
	if g := strings.TrimSpace(current.Goal); g != "" && g != strings.TrimSpace(next.Goal) {
		return false
	}
	have := map[string]bool{}
	for _, line := range strings.Split(next.Instructions, "\n") {
		have[strings.TrimSpace(line)] = true
	}
	for _, line := range strings.Split(current.Instructions, "\n") {
		if line = strings.TrimSpace(line); line != "" && !have[line] {
			return false
		}
	}
	var was, is any
	if len(current.Preferences) == 0 {
		return true
	}
	if json.Unmarshal(current.Preferences, &was) != nil || len(next.Preferences) == 0 || json.Unmarshal(next.Preferences, &is) != nil {
		return false
	}
	return keepsValue(was, is)
}

func keepsValue(was, is any) bool {
	switch w := was.(type) {
	case nil:
		return true
	case string:
		if strings.TrimSpace(w) == "" {
			return true
		}
	case map[string]any:
		now, ok := is.(map[string]any)
		if !ok {
			return len(w) == 0
		}
		for k, v := range w {
			if !keepsValue(v, now[k]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(was, is)
}

// maxOpenQuestions is how many questions one agent may have waiting for
// the owner in a Book, so one agent can't bury another's.
const maxOpenQuestions = 50

// AgentQuestions reads the Book's questions for the owner.
func (a TenantAdmin) AgentQuestions(ctx context.Context, book string, q QuestionQuery) ([]AgentQuestion, error) {
	var out []AgentQuestion
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readQuestions(ctx, tx, s, q)
		return err
	})
	return out, err
}

// AnswerQuestion records the owner's answer. A question already answered is
// replaced: the old answer is superseded and the new one recorded, so the
// history stays.
func (a TenantAdmin) AnswerQuestion(ctx context.Context, book, actor, question string, answer json.RawMessage) (AgentQuestion, error) {
	var out AgentQuestion
	if !ValidID(question) || !json.Valid(answer) || len(answer) == 0 || len(answer) > 4096 {
		return out, ErrInvalid
	}
	err := a.withRuleOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		v, err := scanQuestion(tx.QueryRow(ctx, `SELECT `+questionColumns+` FROM public.agent_questions q WHERE q.tenant_id=$1 AND q.book_id=$2 AND q.id=$3`, s.TenantID, s.BookID, question))
		if err != nil {
			return dbError(err)
		}
		id := v.ID
		switch v.Status {
		case "open":
			_, err = tx.Exec(ctx, "UPDATE public.agent_questions SET status='answered',answer=$4::jsonb,answered_at=clock_timestamp(),answered_by=$5 WHERE tenant_id=$1 AND book_id=$2 AND id=$3",
				s.TenantID, s.BookID, id, string(answer), actor)
		case "answered":
			if _, err = tx.Exec(ctx, "UPDATE public.agent_questions SET status='superseded',reason='The owner changed the answer.' WHERE tenant_id=$1 AND book_id=$2 AND id=$3",
				s.TenantID, s.BookID, id); err != nil {
				return noteError(err)
			}
			id = ID()
			var choices *string
			if v.Choices != nil {
				c := string(v.Choices)
				choices = &c
			}
			_, err = tx.Exec(ctx, `INSERT INTO public.agent_questions(tenant_id,book_id,id,actor_id,subject_kind,subject_key,question,answer,status,answered_at,answered_by,choices)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,'answered',clock_timestamp(),$4,$9::jsonb)`, s.TenantID, s.BookID, id, actor, v.SubjectKind, v.SubjectKey, v.Question, string(answer), choices)
		default:
			return &RefusedError{Reason: "This question was withdrawn or replaced."}
		}
		if err != nil {
			return noteError(err)
		}
		// An answer about which Book a counterparty's charges belong to can
		// become the owner's rule for it.
		var pick struct {
			Value    string `json:"value"`
			MakeRule bool   `json:"make_rule"`
		}
		if v.SubjectKind == "entity" && json.Unmarshal(answer, &pick) == nil && pick.MakeRule {
			account, party, ok := strings.Cut(v.SubjectKey, ",")
			if !ok || account != "" && !ValidID(account) {
				return &RefusedError{Reason: "This question names no bank account and counterparty to make a rule for."}
			}
			if err := setAssignmentRule(ctx, tx, s, actor, AssignmentRuleChange{BankAccountID: account, CounterpartyKey: party, Target: pick.Value}, "answer"); err != nil {
				return err
			}
		}
		out, err = scanQuestion(tx.QueryRow(ctx, `SELECT `+questionColumns+` FROM public.agent_questions q WHERE q.tenant_id=$1 AND q.book_id=$2 AND q.id=$3`, s.TenantID, s.BookID, id))
		return dbError(err)
	})
	return out, err
}

// AgentProfile reads what agents remember about the person, for the owner.
func (a TenantAdmin) AgentProfile(ctx context.Context, book string) (AgentProfile, error) {
	var out AgentProfile
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readProfile(ctx, tx, s)
		return err
	})
	return out, err
}

// SetAgentProfile lets the owner edit what agents remember.
func (a TenantAdmin) SetAgentProfile(ctx context.Context, book, actor string, q UpdateAgentProfile) (AgentProfile, error) {
	var out AgentProfile
	if q.ExpectedRevision < 0 {
		return out, ErrInvalid
	}
	if strings.TrimSpace(q.Reason) == "" {
		q.Reason = "Edited by the owner."
	}
	err := a.withRuleOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		result, err := writeProfile(ctx, tx, s, q)
		if err != nil {
			return err
		}
		return json.Unmarshal(result, &out)
	})
	return out, err
}

// MarkSeen records when the owner last looked, and returns the time before.
func (a TenantAdmin) MarkSeen(ctx context.Context, book, actor string) (*time.Time, error) {
	var before *time.Time
	if !ValidID(actor) {
		return nil, ErrDenied
	}
	err := a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, s Scope, _ Book) error {
		s.ActorID = actor
		if err := setScope(ctx, tx, s); err != nil {
			return dbError(err)
		}
		if err := tx.QueryRow(ctx, "SELECT seen_at FROM public.agent_seen WHERE tenant_id=$1 AND book_id=$2 AND person=$3", s.TenantID, s.BookID, actor).Scan(&before); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return dbError(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO public.agent_seen(tenant_id,book_id,person) VALUES($1,$2,$3) ON CONFLICT (tenant_id,book_id,person) DO UPDATE SET seen_at=clock_timestamp()`,
			s.TenantID, s.BookID, actor)
		return noteError(err)
	})
	return before, err
}

// OwnerReminders lists what a Book asks its owner to do: the open
// reminders, such as adding a receipt or switching a link's terms.
func (a TenantAdmin) OwnerReminders(ctx context.Context, book string) ([]AgentLoop, error) {
	out := []AgentLoop{}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		loops, err := readLoops(ctx, tx, s, LoopQuery{Status: "open"})
		for _, l := range loops {
			if l.Kind == "reminder" {
				out = append(out, l)
			}
		}
		return err
	})
	return out, err
}

// CloseReminder marks one of a Book's reminders done. Any valid sign-in of
// the owner may: it only narrows what is open.
func (a TenantAdmin) CloseReminder(ctx context.Context, book, actor, id string) error {
	if !ValidID(id) || !ValidID(actor) {
		return ErrInvalid
	}
	return a.withAgentNarrowing(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		tag, err := tx.Exec(ctx, `UPDATE public.agent_loops SET status='done',closed_reason='Done by the owner.',closed_at=clock_timestamp()
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND status='open' AND kind='reminder'`, s.TenantID, s.BookID, id)
		if err != nil {
			return noteError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
