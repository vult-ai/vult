package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vult-ai/vult/internal/strictjson"
)

// Project is a light label on what a Book spends: a side project, say,
// paid from personal cards. Labels never change accounts or amounts.
type Project struct {
	BookID    string    `json:"book_id"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	StartsOn  string    `json:"starts_on,omitempty"`
	EndsOn    string    `json:"ends_on,omitempty"`
	Archived  bool      `json:"archived"`
	Revision  int       `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
}

// ProjectChange creates a project, or with ID changes one: its name, its
// dates (empty clears them) or whether it is archived.
type ProjectChange struct {
	RequestKey string  `json:"request_key"`
	Name       *string `json:"name,omitempty"`
	StartsOn   *string `json:"starts_on,omitempty"`
	EndsOn     *string `json:"ends_on,omitempty"`
	Archived   *bool   `json:"archived,omitempty"`
}

// ProjectSplit is one project's part of an entry's spending. LineID names
// the line when the entry spends on more than one account.
type ProjectSplit struct {
	ProjectID   string `json:"project_id"`
	AmountMinor string `json:"amount_minor,omitempty"`
	LineID      string `json:"line_id,omitempty"`
}

// ProjectLabel is a line's part that went to a project.
type ProjectLabel struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	EntryID     string `json:"entry_id"`
	LineID      string `json:"line_id"`
	AmountMinor string `json:"amount_minor"`
	Source      string `json:"source"`
}

// ProjectRule labels every item recorded from a counterparty, optionally on
// one bank account and between two dates.
type ProjectRule struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	CounterpartyKey string `json:"counterparty_key"`
	BankAccountID   string `json:"bank_account_id,omitempty"`
	StartsOn        string `json:"starts_on,omitempty"`
	EndsOn          string `json:"ends_on,omitempty"`
}

const projectColumns = `book_id::text,id::text,name,coalesce(starts_on::text,''),coalesce(ends_on::text,''),archived,revision,created_at`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	err := row.Scan(&p.BookID, &p.ID, &p.Name, &p.StartsOn, &p.EndsOn, &p.Archived, &p.Revision, &p.CreatedAt)
	return p, err
}

func projectError(err error) error {
	if err == nil {
		return nil
	}
	var refused *RefusedError
	if errors.As(err, &refused) {
		return err
	}
	if strings.Contains(err.Error(), "projects_name") {
		return errProjectNameTaken
	}
	return linkError(err)
}

// withProjectWriter runs a change to projects or their labels as the owner.
// Any valid sign-in may: a label never changes an amount.
func (a TenantAdmin) withProjectWriter(ctx context.Context, book, actor string, call func(pgx.Tx, Scope) error) error {
	return a.withRuleOwnerChange(ctx, book, actor, true, call)
}

// Projects lists a Book's projects, archived ones included.
func (a TenantAdmin) Projects(ctx context.Context, book string) ([]Project, error) {
	out := []Project{}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readProjects(ctx, tx, s)
		return err
	})
	return out, err
}

func readProjects(ctx context.Context, tx pgx.Tx, s Scope) ([]Project, error) {
	rows, err := tx.Query(ctx, "SELECT "+projectColumns+" FROM public.projects WHERE tenant_id=$1 AND book_id=$2 ORDER BY archived,lower(name),id", s.TenantID, s.BookID)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, dbError(err)
		}
		out = append(out, p)
	}
	return out, dbError(rows.Err())
}

func validOptionalDate(v *string) bool { return v == nil || *v == "" || ValidDate(*v) }

func nullDate(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}

// errProjectNameTaken refuses a second open project of a name.
var errProjectNameTaken = &RefusedError{Reason: "This Book already has a project with that name."}

// openProjectNamed finds a Book's open project by name, ignoring case and
// surrounding spaces. An archived project isn't found.
func openProjectNamed(ctx context.Context, tx pgx.Tx, s Scope, name string) (Project, bool, error) {
	p, err := scanProject(tx.QueryRow(ctx, "SELECT "+projectColumns+` FROM public.projects
 WHERE tenant_id=$1 AND book_id=$2 AND NOT archived AND lower(btrim(name))=lower($3) ORDER BY created_at,id LIMIT 1`, s.TenantID, s.BookID, strings.TrimSpace(name)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, dbError(err)
	}
	return p, true, nil
}

func validProjectName(name string) bool {
	return strings.TrimSpace(name) != "" && len([]rune(name)) <= 120
}

// insertProject adds a project, refusing a second open project of the same
// name. The caller holds the Book's write lock, so two requests can't both
// find the name free.
func insertProject(ctx context.Context, tx pgx.Tx, s Scope, actor, key string, q ProjectChange) (Project, error) {
	name := strings.TrimSpace(*q.Name)
	archived := q.Archived != nil && *q.Archived
	if !archived {
		if _, taken, err := openProjectNamed(ctx, tx, s, name); err != nil {
			return Project{}, err
		} else if taken {
			return Project{}, errProjectNameTaken
		}
	}
	out, err := scanProject(tx.QueryRow(ctx, `INSERT INTO public.projects(tenant_id,book_id,id,request_key,name,starts_on,ends_on,archived,actor_id)
 VALUES($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9) RETURNING `+projectColumns, s.TenantID, s.BookID, ID(), key, name, nullDate(q.StartsOn), nullDate(q.EndsOn), archived, actor))
	if err != nil {
		if strings.Contains(err.Error(), "ends_on") || strings.Contains(err.Error(), "check constraint") {
			return Project{}, &RefusedError{Reason: "A project ends on or after the day it starts."}
		}
		return Project{}, projectError(err)
	}
	return out, nil
}

// CreateProject adds a project to a Book; asked again with the same
// request key, it answers with the same project.
func (a TenantAdmin) CreateProject(ctx context.Context, book, actor string, q ProjectChange) (Project, error) {
	var out Project
	if q.Name == nil || !validProjectName(*q.Name) || len(q.RequestKey) < 1 || len(q.RequestKey) > 128 ||
		!validOptionalDate(q.StartsOn) || !validOptionalDate(q.EndsOn) {
		return out, ErrInvalid
	}
	err := a.withProjectWriter(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var err error
		out, err = scanProject(tx.QueryRow(ctx, "SELECT "+projectColumns+" FROM public.projects WHERE tenant_id=$1 AND book_id=$2 AND request_key=$3", s.TenantID, s.BookID, q.RequestKey))
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return dbError(err)
		}
		out, err = insertProject(ctx, tx, s, actor, q.RequestKey, q)
		return err
	})
	return out, err
}

// UpdateProject renames a project, changes its dates, or archives it.
func (a TenantAdmin) UpdateProject(ctx context.Context, book, actor, id string, q ProjectChange) (Project, error) {
	var out Project
	if !ValidID(id) || q.Name != nil && (strings.TrimSpace(*q.Name) == "" || len([]rune(*q.Name)) > 120) || !validOptionalDate(q.StartsOn) || !validOptionalDate(q.EndsOn) {
		return out, ErrInvalid
	}
	err := a.withProjectWriter(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var name any
		if q.Name != nil {
			name = strings.TrimSpace(*q.Name)
		}
		var err error
		out, err = scanProject(tx.QueryRow(ctx, `UPDATE public.projects SET name=coalesce($4,name),
 starts_on=CASE WHEN $5 THEN $6::date ELSE starts_on END,ends_on=CASE WHEN $7 THEN $8::date ELSE ends_on END,
 archived=coalesce($9,archived),actor_id=$10 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 RETURNING `+projectColumns,
			s.TenantID, s.BookID, id, name, q.StartsOn != nil, nullDate(q.StartsOn), q.EndsOn != nil, nullDate(q.EndsOn), q.Archived, actor))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil && strings.Contains(err.Error(), "check constraint") {
			return &RefusedError{Reason: "A project ends on or after the day it starts."}
		}
		return projectError(err)
	})
	return out, err
}

// LabelEntry says which projects an entry's spending went to, replacing
// what it said before. No splits takes the labels off. A split with no
// amount takes the whole line; a split with no line names the entry's one
// spending line. Amounts never change.
func (a TenantAdmin) LabelEntry(ctx context.Context, book, actor, entry string, splits []ProjectSplit) ([]ProjectLabel, error) {
	var out []ProjectLabel
	if !ValidID(entry) || len(splits) > 50 {
		return nil, ErrInvalid
	}
	err := a.withProjectWriter(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		if err := labelEntry(ctx, tx, s, actor, entry, splits); err != nil {
			return err
		}
		var err error
		out, err = readEntryLabels(ctx, tx, s, entry)
		return err
	})
	return out, err
}

func labelEntry(ctx context.Context, tx pgx.Tx, s Scope, actor, entry string, splits []ProjectSplit) error {
	type line struct {
		id     string
		amount int64
	}
	rows, err := tx.Query(ctx, `SELECT l.id::text,l.amount_minor FROM public.lines l JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=$3 AND a.class IN ('expense','income') ORDER BY l.position`, s.TenantID, s.BookID, entry)
	if err != nil {
		return dbError(err)
	}
	var lines []line
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.id, &l.amount); err != nil {
			rows.Close()
			return dbError(err)
		}
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dbError(err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND id=$3)", s.TenantID, s.BookID, entry).Scan(&exists); err != nil {
		return dbError(err)
	}
	if !exists {
		return ErrNotFound
	}
	if len(lines) == 0 && len(splits) > 0 {
		return &RefusedError{Reason: "Only spending and income go to a project; this entry moves money between accounts."}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM public.project_labels WHERE tenant_id=$1 AND book_id=$2 AND entry_id=$3", s.TenantID, s.BookID, entry); err != nil {
		return dbError(err)
	}
	seen := map[string]bool{}
	for _, sp := range splits {
		if !ValidID(sp.ProjectID) || sp.LineID != "" && !ValidID(sp.LineID) || sp.AmountMinor != "" && !amountPattern.MatchString(sp.AmountMinor) {
			return ErrInvalid
		}
		target := line{id: sp.LineID}
		if sp.LineID == "" {
			if len(lines) != 1 {
				return &RefusedError{Reason: "This entry spends on more than one account; say which line each part is on."}
			}
			target = lines[0]
		} else {
			for _, l := range lines {
				if l.id == sp.LineID {
					target = l
				}
			}
			if target.amount == 0 {
				return &RefusedError{Reason: "That line isn't spending or income in this entry."}
			}
		}
		amount := target.amount
		if sp.AmountMinor != "" {
			if amount, err = strconv.ParseInt(sp.AmountMinor, 10, 64); err != nil {
				return ErrInvalid
			}
		}
		if seen[target.id+sp.ProjectID] {
			return &RefusedError{Reason: "A project is named twice for the same line; give it one part."}
		}
		seen[target.id+sp.ProjectID] = true
		if _, err := tx.Exec(ctx, `INSERT INTO public.project_labels(tenant_id,book_id,line_id,project_id,entry_id,amount_minor,source,actor_id)
 VALUES($1,$2,$3,$4,$5,$6,'label',$7)`, s.TenantID, s.BookID, target.id, sp.ProjectID, entry, amount, actor); err != nil {
			if strings.Contains(err.Error(), "project_labels_tenant_id_book_id_project_id_fkey") {
				return &RefusedError{Reason: "That project isn't one of this Book's."}
			}
			return projectError(err)
		}
	}
	// The parts must fit their lines now, so a refusal keeps its reason.
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS project_labels_fit IMMEDIATE"); err != nil {
		return projectError(err)
	}
	return nil
}

func readEntryLabels(ctx context.Context, tx pgx.Tx, s Scope, entry string) ([]ProjectLabel, error) {
	rows, err := tx.Query(ctx, `SELECT p.project_id::text,j.name,p.entry_id::text,p.line_id::text,p.amount_minor::text,p.source
 FROM public.project_labels p JOIN public.projects j ON j.tenant_id=p.tenant_id AND j.book_id=p.book_id AND j.id=p.project_id
 WHERE p.tenant_id=$1 AND p.book_id=$2 AND ($3='' OR p.entry_id::text=$3) ORDER BY p.entry_id,p.line_id,lower(j.name)`, s.TenantID, s.BookID, entry)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []ProjectLabel{}
	for rows.Next() {
		var l ProjectLabel
		if err := rows.Scan(&l.ProjectID, &l.ProjectName, &l.EntryID, &l.LineID, &l.AmountMinor, &l.Source); err != nil {
			return nil, dbError(err)
		}
		out = append(out, l)
	}
	return out, dbError(rows.Err())
}

// LabelCharge records an unrecorded bank item to an account and labels it
// for a project, in one step: the owner's "For a project".
func (a TenantAdmin) LabelCharge(ctx context.Context, book, actor, item, account, project string) (string, error) {
	var entry string
	if !ValidID(item) || !ValidID(account) || !ValidID(project) || !ValidID(actor) {
		return "", ErrInvalid
	}
	err := a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, s Scope, _ Book) error {
		s.ActorID = actor
		if err := setScope(ctx, tx, s); err != nil {
			return dbError(err)
		}
		var allowed bool
		if err := tx.QueryRow(ctx, "SELECT public.ledger_import_owner() OR access.owner_decision_allowed($1,$2)", s.TenantID, s.BookID).Scan(&allowed); err != nil || !allowed {
			return ErrFreshOwnerProof
		}
		if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", s.TenantID, s.BookID); err != nil {
			return dbError(err)
		}
		// Asked again for the same item, the answer is the entry already made.
		if err := tx.QueryRow(ctx, "SELECT entry_id::text FROM public.bank_transaction_links WHERE tenant_id=$1 AND book_id=$2 AND transaction_id=$3 AND revoked_at IS NULL",
			s.TenantID, s.BookID, item).Scan(&entry); err == nil {
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return dbError(err)
		}
		var description string
		if err := tx.QueryRow(ctx, "SELECT coalesce(nullif(btrim(counterparty),''),nullif(btrim(description),''),'Bank activity') FROM public.bank_transactions WHERE tenant_id=$1 AND book_id=$2 AND id=$3",
			s.TenantID, s.BookID, item).Scan(&description); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return dbError(err)
		}
		result, err := dispatch(ctx, tx, s, "record_bank_activity", raw(RecordBankActivity{RequestKey: "project-" + item, TransactionID: item,
			Description: clip(description, 200), OtherAccountID: account}), nil)
		if err != nil {
			return err
		}
		if entry = outcomeEntry(&Outcome{Result: result}); entry == "" {
			return ErrUncertain
		}
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return bankError(err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM public.project_labels WHERE tenant_id=$1 AND book_id=$2 AND entry_id=$3", s.TenantID, s.BookID, entry); err != nil {
			return dbError(err)
		}
		return labelEntry(ctx, tx, s, actor, entry, []ProjectSplit{{ProjectID: project}})
	})
	return entry, err
}

// EntryProject is a project's part of the spending a bank item recorded.
type EntryProject struct {
	ProjectID   string `json:"project_id,omitempty"`
	Name        string `json:"name"`
	AmountMinor string `json:"amount_minor"`
	// New marks a project the request named as new: it is created when the
	// item is recorded, unless an open project has taken the name by then.
	New bool `json:"new,omitempty"`
}

// labelRecorded labels the spending of an entry that has just recorded a
// bank item. Only money going out takes a project, on the entry's one line
// that debits an expense account: decided by account types, never names.
func labelRecorded(ctx context.Context, tx pgx.Tx, s Scope, v bankItem, q RecordBankActivity, entry, receipt string) ([]EntryProject, error) {
	if v.debitPositive() >= 0 {
		return nil, &RefusedError{Reason: "Only money spent goes to a project, and this item is money coming in. Record it without a project."}
	}
	rows, err := tx.Query(ctx, `SELECT l.id::text FROM public.lines l JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=$3 AND a.class='expense' AND l.side='debit' ORDER BY l.position`, s.TenantID, s.BookID, entry)
	if err != nil {
		return nil, dbError(err)
	}
	var spending []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		spending = append(spending, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	switch {
	case len(spending) == 0:
		return nil, &RefusedError{Reason: "Only spending goes to a project, and this entry has no line on an expense account. Record it without a project."}
	case len(spending) > 1:
		return nil, &RefusedError{Reason: "This entry spends on more than one account. Record it without a project, then say each line's project with label_project."}
	}
	splits := q.ProjectSplits
	switch {
	case q.NewProjectName != "":
		project, found, err := openProjectNamed(ctx, tx, s, q.NewProjectName)
		if err != nil {
			return nil, err
		}
		if !found {
			if !ownerApproves(ctx) {
				return nil, &RefusedError{Reason: "A new project needs the owner's approval. Prepare this with prepare_operation, or give the project_id of a project from list_projects."}
			}
			name := q.NewProjectName
			if project, err = insertProject(ctx, tx, s, s.ActorID, receipt, ProjectChange{Name: &name}); err != nil {
				return nil, err
			}
		}
		splits = []ProjectSplit{{ProjectID: project.ID}}
	case q.ProjectID != "":
		splits = []ProjectSplit{{ProjectID: q.ProjectID}}
	}
	parts := make([]ProjectSplit, len(splits))
	for i, sp := range splits {
		parts[i] = ProjectSplit{ProjectID: sp.ProjectID, AmountMinor: sp.AmountMinor, LineID: spending[0]}
	}
	if err := labelEntry(ctx, tx, s, s.ActorID, entry, parts); err != nil {
		return nil, err
	}
	labels, err := readEntryLabels(ctx, tx, s, entry)
	if err != nil {
		return nil, err
	}
	out := make([]EntryProject, 0, len(labels))
	for _, l := range labels {
		p := EntryProject{ProjectID: l.ProjectID, Name: l.ProjectName, AmountMinor: l.AmountMinor}
		if q.NewProjectName != "" {
			// The name is the request's own, so a review reads the same
			// whichever request created the project.
			p.Name, p.New = q.NewProjectName, true
		}
		out = append(out, p)
	}
	return out, nil
}

// newProjectNameFree refuses preparing a bank item for a new project whose
// name an open project already has, naming that project to use instead.
func newProjectNameFree(ctx context.Context, tx pgx.Tx, s Scope, operation string, args json.RawMessage) error {
	if operation != "record_bank_activity" {
		return nil
	}
	var q RecordBankActivity
	if json.Unmarshal(args, &q) != nil || q.NewProjectName == "" {
		return nil
	}
	project, found, err := openProjectNamed(ctx, tx, s, q.NewProjectName)
	if err != nil || !found {
		return err
	}
	return &RefusedError{Reason: "This Book already has a project named " + project.Name + ". Give project_id " + project.ID + " instead of new_project_name."}
}

// ProjectRules lists a Book's project rules.
func (a TenantAdmin) ProjectRules(ctx context.Context, book string) ([]ProjectRule, error) {
	out := []ProjectRule{}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		rows, err := tx.Query(ctx, `SELECT id::text,project_id::text,counterparty_key,coalesce(bank_account_id::text,''),coalesce(starts_on::text,''),coalesce(ends_on::text,'')
 FROM public.project_rules WHERE tenant_id=$1 AND book_id=$2 ORDER BY created_at,id`, s.TenantID, s.BookID)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var r ProjectRule
			if err := rows.Scan(&r.ID, &r.ProjectID, &r.CounterpartyKey, &r.BankAccountID, &r.StartsOn, &r.EndsOn); err != nil {
				return dbError(err)
			}
			out = append(out, r)
		}
		return dbError(rows.Err())
	})
	return out, err
}

// SetProjectRule adds a rule labelling every item recorded from a
// counterparty as a project. Counterparty is the name as the bank shows it.
func (a TenantAdmin) SetProjectRule(ctx context.Context, book, actor string, q ProjectRule) (ProjectRule, error) {
	if !ValidID(q.ProjectID) || strings.TrimSpace(q.CounterpartyKey) == "" || len(q.CounterpartyKey) > 200 || q.BankAccountID != "" && !ValidID(q.BankAccountID) ||
		q.StartsOn != "" && !ValidDate(q.StartsOn) || q.EndsOn != "" && !ValidDate(q.EndsOn) {
		return ProjectRule{}, ErrInvalid
	}
	err := a.withProjectWriter(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var account any
		if q.BankAccountID != "" {
			account = q.BankAccountID
		}
		q.ID = ID()
		err := tx.QueryRow(ctx, `INSERT INTO public.project_rules(tenant_id,book_id,id,project_id,counterparty_key,bank_account_id,starts_on,ends_on,actor_id)
 VALUES($1,$2,$3,$4,public.rule_key($5),$6,nullif($7,'')::date,nullif($8,'')::date,$9) RETURNING counterparty_key`,
			s.TenantID, s.BookID, q.ID, q.ProjectID, q.CounterpartyKey, account, q.StartsOn, q.EndsOn, actor).Scan(&q.CounterpartyKey)
		if err != nil && strings.Contains(err.Error(), "check constraint") {
			return &RefusedError{Reason: "A rule ends on or after the day it starts."}
		}
		return projectError(err)
	})
	return q, err
}

// DeleteProjectRule removes a rule; what it labelled stays labelled.
func (a TenantAdmin) DeleteProjectRule(ctx context.Context, book, actor, id string) error {
	if !ValidID(id) {
		return ErrInvalid
	}
	return a.withProjectWriter(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		tag, err := tx.Exec(ctx, "DELETE FROM public.project_rules WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, id)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return dbError(err)
	})
}

// ProjectReport is what each project cost, by year and by account, with
// the receipts it is missing, and the spending in no project.
type ProjectReport struct {
	Years       []int          `json:"years"`
	Projects    []ProjectYears `json:"projects"`
	Unlabelled  []YearAmount   `json:"unlabelled"`
	ReceiptOver string         `json:"receipt_over_minor"`
}

type ProjectYears struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// CarriedMinor is what was carried into a company made from it.
	CarriedMinor string        `json:"carried_minor"`
	ByYear       []ProjectYear `json:"by_year"`
}

type ProjectYear struct {
	Year        int          `json:"year"`
	TotalMinor  string       `json:"total_minor"`
	ByKind      []KindAmount `json:"by_kind"`
	ReceiptGaps ReceiptGaps  `json:"receipt_gaps"`
}

type KindAmount struct {
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	AmountMinor string `json:"amount_minor"`
}

type ReceiptGaps struct {
	Count     int    `json:"count"`
	OverMinor string `json:"over_minor"`
}

type YearAmount struct {
	Year        int    `json:"year"`
	AmountMinor string `json:"amount_minor"`
}

// ProjectReport reads what each project cost. Spending counts debits on
// expense accounts, less credits (refunds). A receipt is missing when the
// entry has no attached receipt or source, for parts over 75 in the Book's
// currency.
func (a TenantAdmin) ProjectReport(ctx context.Context, book string, year int) (ProjectReport, error) {
	out := ProjectReport{Years: []int{}, Projects: []ProjectYears{}, Unlabelled: []YearAmount{}}
	if year != 0 && (year < 1900 || year > 2200) {
		return out, ErrInvalid
	}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, b Book) error {
		var err error
		out, err = projectReport(ctx, tx, s, b, year)
		return err
	})
	return out, err
}

// projectMethod reports whether an agent's tool is a project tool: reading
// projects and the report, and labelling entries (agents that record).
func projectMethod(method string) bool {
	return method == "list_projects" || method == "label_project" || method == "get_project_report"
}

func projectCall(ctx context.Context, tx pgx.Tx, s Scope, method string, input json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "list_projects":
		var q struct{}
		if len(input) > 0 && strictjson.Decode(input, &q) != nil {
			return nil, ErrInvalid
		}
		projects, err := readProjects(ctx, tx, s)
		if err != nil {
			return nil, err
		}
		labels, err := readEntryLabels(ctx, tx, s, "")
		return raw(map[string]any{"projects": projects, "labelled_lines": len(labels)}), err
	case "label_project":
		var q struct {
			RequestKey string         `json:"request_key"`
			EntryID    string         `json:"entry_id"`
			ProjectID  *string        `json:"project_id,omitempty"`
			Splits     []ProjectSplit `json:"splits,omitempty"`
		}
		if strictjson.Decode(input, &q) != nil || len(q.RequestKey) < 1 || len(q.RequestKey) > 128 || !ValidID(q.EntryID) {
			return nil, ErrInvalid
		}
		splits := q.Splits
		if splits == nil && q.ProjectID != nil {
			splits = []ProjectSplit{{ProjectID: *q.ProjectID}}
		}
		if err := labelEntry(ctx, tx, s, s.ActorID, q.EntryID, splits); err != nil {
			return nil, err
		}
		labels, err := readEntryLabels(ctx, tx, s, q.EntryID)
		return raw(map[string]any{"entry_id": q.EntryID, "labels": labels}), err
	case "get_project_report":
		var q struct {
			Year int `json:"year,omitempty"`
		}
		if len(input) > 0 && strictjson.Decode(input, &q) != nil {
			return nil, ErrInvalid
		}
		book, err := readBook(ctx, tx, s)
		if err != nil {
			return nil, dbError(err)
		}
		report, err := projectReport(ctx, tx, s, book, q.Year)
		return raw(report), err
	}
	return nil, ErrDenied
}

func projectReport(ctx context.Context, tx pgx.Tx, s Scope, b Book, year int) (ProjectReport, error) {
	out := ProjectReport{Years: []int{}, Projects: []ProjectYears{}, Unlabelled: []YearAmount{}}
	if year != 0 && (year < 1900 || year > 2200) {
		return out, ErrInvalid
	}
	err := func() error {
		over := int64(75)
		for i := 0; i < b.Scale; i++ {
			over *= 10
		}
		out.ReceiptOver = strconv.FormatInt(over, 10)
		var data []byte
		err := tx.QueryRow(ctx, `WITH spend AS (
    SELECT l.id AS line_id, l.entry_id, l.account_id, a.name AS account_name, extract(year FROM e.accounting_date)::int AS year,
        CASE WHEN l.side='debit' THEN l.amount_minor ELSE -l.amount_minor END AS amount
    FROM public.lines l JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
    JOIN public.entries e ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
    WHERE l.tenant_id=$1 AND l.book_id=$2 AND a.class='expense' AND ($3=0 OR extract(year FROM e.accounting_date)=$3)
        AND NOT EXISTS (SELECT 1 FROM public.project_labels c WHERE c.tenant_id=l.tenant_id AND c.book_id=l.book_id AND c.line_id=l.id AND c.source='carried')
), labelled AS (
    SELECT p.project_id, s.*, CASE WHEN s.amount<0 THEN -p.amount_minor ELSE p.amount_minor END AS part,
        NOT EXISTS (SELECT 1 FROM public.evidence_links v WHERE v.tenant_id=$1 AND v.book_id=$2 AND v.entry_id=s.entry_id AND v.action='attach'
                AND NOT EXISTS (SELECT 1 FROM public.evidence_links w WHERE w.tenant_id=v.tenant_id AND w.book_id=v.book_id AND w.link_id=v.link_id AND w.revision>v.revision))
            AND NOT EXISTS (SELECT 1 FROM public.source_citations c WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.entry_id=s.entry_id) AS no_receipt
    FROM public.project_labels p JOIN spend s ON s.line_id=p.line_id WHERE p.tenant_id=$1 AND p.book_id=$2
)
SELECT jsonb_build_object(
    'years', coalesce((SELECT jsonb_agg(DISTINCT y.year ORDER BY y.year) FROM spend y), '[]'::jsonb),
    'projects', coalesce((SELECT jsonb_agg(jsonb_build_object('id', j.id, 'name', j.name,
        'carried_minor', coalesce((SELECT sum(c.amount_minor) FROM public.project_labels c WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.project_id=j.id AND c.source='carried'), 0)::text,
        'by_year', coalesce((
        SELECT jsonb_agg(jsonb_build_object('year', g.year, 'total_minor', g.total::text,
            'by_kind', (SELECT jsonb_agg(jsonb_build_object('account_id', k.account_id, 'account_name', k.account_name, 'amount_minor', k.total::text) ORDER BY k.account_name)
                FROM (SELECT x.account_id, x.account_name, sum(x.part) AS total FROM labelled x WHERE x.project_id=j.id AND x.year=g.year GROUP BY x.account_id, x.account_name) k),
            'receipt_gaps', jsonb_build_object('count', (SELECT count(*) FROM labelled x WHERE x.project_id=j.id AND x.year=g.year AND x.no_receipt AND x.part>$4),
                'over_minor', $4::text)) ORDER BY g.year)
        FROM (SELECT x.year, sum(x.part) AS total FROM labelled x WHERE x.project_id=j.id GROUP BY x.year) g), '[]'::jsonb)) ORDER BY j.archived, lower(j.name))
        FROM public.projects j WHERE j.tenant_id=$1 AND j.book_id=$2), '[]'::jsonb),
    'unlabelled', coalesce((SELECT jsonb_agg(jsonb_build_object('year', u.year, 'amount_minor', u.total::text) ORDER BY u.year)
        FROM (SELECT y.year, sum(y.amount) - coalesce((SELECT sum(x.part) FROM labelled x WHERE x.year=y.year), 0) AS total FROM spend y GROUP BY y.year) u WHERE u.total<>0), '[]'::jsonb))`,
			s.TenantID, s.BookID, year, over).Scan(&data)
		if err != nil {
			return dbError(err)
		}
		receiptOver := out.ReceiptOver
		if json.Unmarshal(data, &out) != nil {
			return ErrUncertain
		}
		out.ReceiptOver = receiptOver
		return nil
	}()
	return out, err
}

// itemProjects says, for each bank item on a page, the project its
// spending went to, or the project it likely belongs to: a rule for its
// counterparty in its date range, else the project the counterparty's
// recorded items were last labelled for.
func itemProjects(ctx context.Context, tx pgx.Tx, s Scope, items []BankTransaction) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, v := range items {
		ids = append(ids, v.ID)
	}
	rows, err := tx.Query(ctx, `WITH item AS (
    SELECT t.id, t.occurred_on, t.bank_account_id, public.rule_key(coalesce(nullif(t.counterparty,''),t.description)) AS key,
        (SELECT k.entry_id FROM public.bank_transaction_links k WHERE k.tenant_id=t.tenant_id AND k.book_id=t.book_id AND k.transaction_id=t.id AND k.revoked_at IS NULL) AS entry
    FROM public.bank_transactions t WHERE t.tenant_id=$1 AND t.book_id=$2 AND t.id = ANY($3::uuid[])
)
SELECT i.id::text,
    (SELECT CASE WHEN count(DISTINCT p.project_id)=1 THEN min(p.project_id::text) END FROM public.project_labels p WHERE p.tenant_id=$1 AND p.book_id=$2 AND p.entry_id=i.entry),
    CASE WHEN i.entry IS NULL THEN (SELECT r.project_id::text FROM public.project_rules r JOIN public.projects j ON j.tenant_id=r.tenant_id AND j.book_id=r.book_id AND j.id=r.project_id
        WHERE r.tenant_id=$1 AND r.book_id=$2 AND NOT j.archived AND r.counterparty_key=i.key
            AND (r.bank_account_id IS NULL OR r.bank_account_id=i.bank_account_id)
            AND (r.starts_on IS NULL OR i.occurred_on>=r.starts_on) AND (r.ends_on IS NULL OR i.occurred_on<=r.ends_on)
        ORDER BY r.created_at DESC LIMIT 1) END,
    CASE WHEN i.entry IS NULL THEN (SELECT p.project_id::text||'/'||l.account_id::text FROM public.project_labels p
        JOIN public.lines l ON l.tenant_id=p.tenant_id AND l.book_id=p.book_id AND l.id=p.line_id
        JOIN public.bank_transaction_links k ON k.tenant_id=p.tenant_id AND k.book_id=p.book_id AND k.entry_id=p.entry_id AND k.revoked_at IS NULL
        JOIN public.bank_transactions t ON t.tenant_id=k.tenant_id AND t.book_id=k.book_id AND t.id=k.transaction_id
        JOIN public.projects j ON j.tenant_id=p.tenant_id AND j.book_id=p.book_id AND j.id=p.project_id
        WHERE p.tenant_id=$1 AND p.book_id=$2 AND NOT j.archived AND public.rule_key(coalesce(nullif(t.counterparty,''),t.description))=i.key
        ORDER BY t.occurred_on DESC, p.labelled_at DESC LIMIT 1) END
FROM item i`, s.TenantID, s.BookID, ids)
	if err != nil {
		return dbError(err)
	}
	type found struct{ project, rule, history *string }
	byItem := map[string]found{}
	for rows.Next() {
		var id string
		var f found
		if err := rows.Scan(&id, &f.project, &f.rule, &f.history); err != nil {
			rows.Close()
			return dbError(err)
		}
		byItem[id] = f
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dbError(err)
	}
	names := map[string]string{}
	projects, err := readProjects(ctx, tx, s)
	if err != nil {
		return err
	}
	for _, p := range projects {
		names[p.ID] = p.Name
	}
	for i, v := range items {
		f := byItem[v.ID]
		switch {
		case f.project != nil:
			items[i].Project = &ProjectRef{ID: *f.project, Name: names[*f.project]}
		case f.rule != nil:
			items[i].SuggestedProject = &ProjectSuggestion{ID: *f.rule, Name: names[*f.rule], Why: "rule"}
		case f.history != nil:
			project, account, _ := strings.Cut(*f.history, "/")
			items[i].SuggestedProject = &ProjectSuggestion{ID: project, Name: names[project], AccountID: account, Why: "history"}
		}
	}
	return nil
}

// PromotionCost is a cost labelled for a project that turning it into a
// company may carry: the entry, its date and description, the project's
// part of it, and whether it falls before the formation date.
type PromotionCost struct {
	EntryID         string `json:"entry_id"`
	Date            string `json:"date"`
	Description     string `json:"description"`
	AmountMinor     string `json:"amount_minor"`
	BeforeFormation bool   `json:"before_formation"`
}

// PromotionPreview lists a project's costs for turning it into a company,
// oldest first; the ones before the formation date are proposed.
func (a TenantAdmin) PromotionPreview(ctx context.Context, book, project, formation string) ([]PromotionCost, error) {
	out := []PromotionCost{}
	if !ValidID(project) || !ValidDate(formation) {
		return out, ErrInvalid
	}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.projects WHERE tenant_id=$1 AND book_id=$2 AND id=$3)", s.TenantID, s.BookID, project).Scan(&exists); err != nil {
			return dbError(err)
		}
		if !exists {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, `SELECT p.entry_id::text,e.accounting_date::text,e.description,
 sum(CASE WHEN l.side='debit' THEN p.amount_minor ELSE -p.amount_minor END)::text,e.accounting_date<$4::date
 FROM public.project_labels p JOIN public.lines l ON l.tenant_id=p.tenant_id AND l.book_id=p.book_id AND l.id=p.line_id
 JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
 JOIN public.entries e ON e.tenant_id=p.tenant_id AND e.book_id=p.book_id AND e.id=p.entry_id
 WHERE p.tenant_id=$1 AND p.book_id=$2 AND p.project_id=$3 AND a.class='expense' AND p.source<>'carried'
   AND NOT EXISTS (SELECT 1 FROM public.project_carried c WHERE c.tenant_id=p.tenant_id AND c.book_id=p.book_id AND c.entry_id=p.entry_id)
 GROUP BY p.entry_id,e.accounting_date,e.sequence,e.description HAVING sum(CASE WHEN l.side='debit' THEN p.amount_minor ELSE -p.amount_minor END)>0
 ORDER BY e.accounting_date,e.sequence`, s.TenantID, s.BookID, project, formation)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var c PromotionCost
			if err := rows.Scan(&c.EntryID, &c.Date, &c.Description, &c.AmountMinor, &c.BeforeFormation); err != nil {
				return dbError(err)
			}
			out = append(out, c)
		}
		return dbError(rows.Err())
	})
	return out, err
}

// PromoteProject turns a project into a company: a new business Book,
// taxed as given, linked to the Book that paid, which records each chosen
// cost as owed back by the company.
type PromoteProject struct {
	RequestKey    string   `json:"request_key"`
	Name          string   `json:"name"`
	TaxedAs       string   `json:"taxed_as"`
	FormationDate string   `json:"formation_date"`
	EntryIDs      []string `json:"entry_ids"`
}

// PromotionResult names the company Book, its link, and the carried pairs.
type PromotionResult struct {
	BookID string   `json:"book_id"`
	LinkID string   `json:"link_id"`
	Pairs  []string `json:"pairs"`
}

// Promote turns a project into a company. The Book that paid moves each
// chosen cost from its expense to what the company owes it, and the
// company records it as a start-up cost it owes back, dated the formation
// date: one carried pair per cost, repaid like any cost. Nothing earlier
// is rewritten, and the project's labels stay. The caller runs it in one
// transaction, so all of it is recorded or none.
func (a TenantAdmin) Promote(ctx context.Context, book, actor, project string, q PromoteProject) (PromotionResult, error) {
	var out PromotionResult
	q.Name = strings.TrimSpace(q.Name)
	if !ValidID(book) || !ValidID(actor) || !ValidID(project) || len(q.RequestKey) < 1 || len(q.RequestKey) > 128 || q.Name == "" || len([]rune(q.Name)) > 120 ||
		!ValidDate(q.FormationDate) || len(q.EntryIDs) == 0 || len(q.EntryIDs) > 500 {
		return out, ErrInvalid
	}
	switch q.TaxedAs {
	case TaxedSoleOwner, TaxedPartnership, TaxedSCorporation, TaxedCCorporation:
	default:
		return out, ErrInvalid
	}
	// Asked again, the answer is the company already made.
	var done bool
	if err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		err := tx.QueryRow(ctx, "SELECT company_book_id::text FROM public.project_promotions WHERE tenant_id=$1 AND book_id=$2 AND request_key=$3", s.TenantID, s.BookID, q.RequestKey).Scan(&out.BookID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		done = err == nil
		return dbError(err)
	}); err != nil {
		return out, err
	}
	if done {
		return out, a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
			rows, err := tx.Query(ctx, "SELECT pair_key FROM public.project_carried WHERE tenant_id=$1 AND book_id=$2 AND request_key=$3 ORDER BY pair_key", s.TenantID, s.BookID, q.RequestKey)
			if err != nil {
				return dbError(err)
			}
			defer rows.Close()
			for rows.Next() {
				var k string
				if err := rows.Scan(&k); err != nil {
					return dbError(err)
				}
				out.Pairs = append(out.Pairs, k)
			}
			return dbError(rows.Err())
		})
	}
	source, err := a.Book(ctx, book)
	if err != nil {
		return out, err
	}
	costs, err := a.PromotionPreview(ctx, book, project, q.FormationDate)
	if err != nil {
		return out, err
	}
	chosen := map[string]PromotionCost{}
	for _, c := range costs {
		chosen[c.EntryID] = c
	}
	for _, id := range q.EntryIDs {
		if _, ok := chosen[id]; !ok {
			return out, &RefusedError{Reason: "Choose costs labelled for this project."}
		}
	}
	// The company Book, a business taxed as the owner said.
	company, err := a.CreateBook(ctx, q.Name, source.Currency, source.Scale)
	if err != nil {
		return out, err
	}
	out.BookID = company.ID
	business, taxed := "business", q.TaxedAs
	if _, err := a.ReviseBook(ctx, actor, ReviseBookSettings{BookID: company.ID, RequestKey: "promote-" + q.RequestKey, Revision: 0, Name: company.Name, Profile: &business, TaxedAs: &taxed}, nil); err != nil {
		return out, err
	}
	account := func(b, class, role, name string) (string, error) {
		result, err := a.AccountChange(ctx, b, actor, "create_account", raw(CreateAccount{RequestKey: "promote-" + q.RequestKey + "-" + role + "-" + b, Class: class, Name: clip(name, 120), Role: role}))
		if err != nil {
			return "", err
		}
		var created Account
		if json.Unmarshal(result, &created) != nil || created.ID == "" {
			return "", ErrUncertain
		}
		return created.ID, nil
	}
	receivable, err := account(book, "asset", "receivable", "Owed by "+q.Name)
	if err != nil {
		return out, err
	}
	payable, err := account(company.ID, "liability", "payable", "Owed to "+source.Name)
	if err != nil {
		return out, err
	}
	startup, err := account(company.ID, "expense", "", "Start-up costs")
	if err != nil {
		return out, err
	}
	link, err := a.SetLinkTerms(ctx, actor, LinkTermsChange{BookIDs: [2]string{book, company.ID}, PayerBookID: book, Treatment: "owed",
		PayerAccountID: receivable, BeneficiaryAccountID: payable, Active: true})
	if err != nil {
		return out, err
	}
	out.LinkID = link.ID
	err = a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		var fresh bool
		if err := tx.QueryRow(ctx, "SELECT access.owner_write_allowed($1)", scope.TenantID).Scan(&fresh); err != nil || !fresh {
			return ErrFreshOwnerProof
		}
		payer := Scope{TenantID: scope.TenantID, BookID: book, ActorID: actor}
		beneficiary := Scope{TenantID: scope.TenantID, BookID: company.ID, ActorID: actor}
		locks := []Scope{payer, beneficiary}
		slices.SortFunc(locks, func(x, y Scope) int { return strings.Compare(x.BookID, y.BookID) })
		for _, b := range locks {
			if err := setScope(ctx, tx, b); err != nil {
				return dbError(err)
			}
			if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", b.TenantID, b.BookID); err != nil {
				return dbError(err)
			}
		}
		if err := setScope(ctx, tx, payer); err != nil {
			return dbError(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.project_promotions(tenant_id,book_id,project_id,request_key,company_book_id,formation_date,actor_id)
 VALUES($1,$2,$3,$4,$5,$6::date,$7)`, scope.TenantID, book, project, q.RequestKey, company.ID, q.FormationDate, actor); err != nil {
			return projectError(err)
		}
		post := func(s Scope, key, description string, lines []PostLine) (string, error) {
			if err := setScope(ctx, tx, s); err != nil {
				return "", dbError(err)
			}
			result, err := dispatch(ctx, tx, s, "post_journal", raw(PostJournal{RequestKey: key, EconomicDate: q.FormationDate, AccountingDate: q.FormationDate,
				Description: clip(description, 200), Lines: lines}), nil)
			if err != nil {
				return "", err
			}
			entry := outcomeEntry(&Outcome{Result: result})
			if entry == "" {
				return "", ErrUncertain
			}
			if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
				return "", dbError(err)
			}
			_, err = tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED")
			return entry, dbError(err)
		}
		for i, id := range q.EntryIDs {
			cost := chosen[id]
			// The project's part of each expense line of the entry.
			if err := setScope(ctx, tx, payer); err != nil {
				return dbError(err)
			}
			rows, err := tx.Query(ctx, `SELECT l.account_id::text,sum(CASE WHEN l.side='debit' THEN p.amount_minor ELSE -p.amount_minor END)
 FROM public.project_labels p JOIN public.lines l ON l.tenant_id=p.tenant_id AND l.book_id=p.book_id AND l.id=p.line_id
 JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
 WHERE p.tenant_id=$1 AND p.book_id=$2 AND p.entry_id=$3 AND p.project_id=$4 AND a.class='expense' AND p.source<>'carried'
 GROUP BY l.account_id HAVING sum(CASE WHEN l.side='debit' THEN p.amount_minor ELSE -p.amount_minor END)>0 ORDER BY l.account_id`,
				scope.TenantID, book, id, project)
			if err != nil {
				return dbError(err)
			}
			var parts []PostLine
			for rows.Next() {
				var acc string
				var amount int64
				if err := rows.Scan(&acc, &amount); err != nil {
					rows.Close()
					return dbError(err)
				}
				parts = append(parts, PostLine{AccountID: acc, Side: "credit", AmountMinor: strconv.FormatInt(amount, 10)})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return dbError(err)
			}
			key := "carry-" + q.RequestKey + "-" + strconv.Itoa(i+1)
			moved, err := post(payer, key+"/payer", "Carried to "+q.Name+": "+cost.Description,
				append([]PostLine{{AccountID: receivable, Side: "debit", AmountMinor: cost.AmountMinor}}, parts...))
			if err != nil {
				return err
			}
			owed, err := post(beneficiary, key+"/company", "Start-up cost paid by "+source.Name+": "+cost.Description,
				[]PostLine{{AccountID: startup, Side: "debit", AmountMinor: cost.AmountMinor}, {AccountID: payable, Side: "credit", AmountMinor: cost.AmountMinor}})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "SELECT public.carry_cost($1,$2,$3,$4,$5,$6)", scope.TenantID, key, book, moved, company.ID, owed); err != nil {
				return linkError(err)
			}
			// The moved lines keep the project's label, as carried, so the
			// project's report still shows what it cost.
			if err := setScope(ctx, tx, payer); err != nil {
				return dbError(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO public.project_labels(tenant_id,book_id,line_id,project_id,entry_id,amount_minor,source,actor_id)
 SELECT l.tenant_id,l.book_id,l.id,$4,l.entry_id,l.amount_minor,'carried',$5 FROM public.lines l
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=$3 AND l.side='credit'`, scope.TenantID, book, moved, project, actor); err != nil {
				return projectError(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO public.project_carried(tenant_id,book_id,entry_id,project_id,request_key,pair_key,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7)`,
				scope.TenantID, book, id, project, q.RequestKey, key, actor); err != nil {
				if strings.Contains(err.Error(), "project_carried_pkey") {
					return &RefusedError{Reason: "One of these costs is already carried into a company."}
				}
				return projectError(err)
			}
			out.Pairs = append(out.Pairs, key)
		}
		return nil
	})
	return out, err
}

// UndoPromotion reverses every carried pair of a promotion, in both Books,
// and takes off their carried labels. The company Book and its link stay
// for the owner to delete. A cost already repaid can't be undone.
func (a TenantAdmin) UndoPromotion(ctx context.Context, book, actor, requestKey string) error {
	if !ValidID(book) || !ValidID(actor) || len(requestKey) < 1 || len(requestKey) > 128 {
		return ErrInvalid
	}
	type carried struct{ pair, moved, formation, company, owed string }
	var all []carried
	if err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		rows, err := tx.Query(ctx, `SELECT c.pair_key,p.payer_entry_id::text,m.formation_date::text,p.beneficiary_book_id::text,p.beneficiary_entry_id::text,
 EXISTS(SELECT 1 FROM public.allocations r WHERE r.tenant_id=$1 AND r.obligation_book_id=p.beneficiary_book_id AND r.obligation_entry_id=p.beneficiary_entry_id
   AND r.removed_at IS NULL AND (r.source_entry_id IS NULL OR NOT EXISTS(SELECT 1 FROM public.entries x WHERE x.tenant_id=r.tenant_id AND x.book_id=r.source_book_id AND x.reversal_of=r.source_entry_id)))
 FROM public.project_carried c JOIN public.project_promotions m ON m.tenant_id=c.tenant_id AND m.book_id=c.book_id AND m.request_key=c.request_key
 JOIN public.entry_pairs p ON p.tenant_id=c.tenant_id AND p.pair_key=c.pair_key
 WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.request_key=$3 ORDER BY c.pair_key`, s.TenantID, s.BookID, requestKey)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var c carried
			var repaid bool
			if err := rows.Scan(&c.pair, &c.moved, &c.formation, &c.company, &c.owed, &repaid); err != nil {
				return dbError(err)
			}
			if repaid {
				return &RefusedError{Reason: "Some of these costs are already repaid; undo those repayments first."}
			}
			all = append(all, c)
		}
		return dbError(rows.Err())
	}); err != nil {
		return err
	}
	if len(all) == 0 {
		return ErrNotFound
	}
	for _, c := range all {
		if _, err := a.AccountChange(ctx, c.company, actor, "reverse_journal", raw(ReverseJournal{RequestKey: "undo-" + c.pair, EntryID: c.owed, AccountingDate: c.formation,
			Reason: "Undo turning the project into a company"})); err != nil {
			return err
		}
	}
	return a.withProjectWriter(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		if _, err := tx.Exec(ctx, `DELETE FROM public.project_labels p USING public.project_carried c, public.entry_pairs e
 WHERE p.tenant_id=$1 AND p.book_id=$2 AND p.source='carried' AND c.tenant_id=p.tenant_id AND c.book_id=p.book_id AND c.request_key=$3
   AND e.tenant_id=c.tenant_id AND e.pair_key=c.pair_key AND p.entry_id=e.payer_entry_id`, s.TenantID, s.BookID, requestKey); err != nil {
			return dbError(err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM public.project_carried WHERE tenant_id=$1 AND book_id=$2 AND request_key=$3", s.TenantID, s.BookID, requestKey); err != nil {
			return dbError(err)
		}
		_, err := tx.Exec(ctx, "DELETE FROM public.project_promotions WHERE tenant_id=$1 AND book_id=$2 AND request_key=$3", s.TenantID, s.BookID, requestKey)
		return dbError(err)
	})
}
