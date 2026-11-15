package ledger

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The built-in agent's settings: whether it is on in a Book, the
// proposing connection it works through, and how far the owner lets it
// record on its own. A Vult keeps no model and no model key; the agent
// itself is a service the Vult is connected to. It works with the same
// tools and grants as any agent.

// AgentSettingsChange is the owner's choice for a Book: its agent on or off.
type AgentSettingsChange struct {
	Enabled          bool
	ExpectedRevision int64
}

// AgentSettings is what the owner sees.
type AgentSettings struct {
	Enabled      bool      `json:"enabled"`
	ConnectionID string    `json:"connection_id"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`
	// RecordingMode is how the built-in agent records directly: "rules",
	// "confident", or empty for drafts only.
	RecordingMode string `json:"recording_mode"`
	// Skills is the agent's skills test result, if it ran.
	Skills *AgentSkills `json:"skills,omitempty"`
	// DefaultTrust is where kinds with no history start: record once the
	// agent passed the skills test, draft otherwise.
	DefaultTrust string `json:"default_trust"`
}

// AgentSkills is how the agent did on the skills test, and the level it
// starts at.
type AgentSkills struct {
	Level    string    `json:"level"`
	Score    int       `json:"score"`
	Total    int       `json:"total"`
	Model    string    `json:"model"`
	TestedAt time.Time `json:"tested_at"`
}

// AgentSecret is what the agent's runner reads: whether the Book's agent is
// on and the connection it works through.
type AgentSecret struct {
	Enabled               bool
	ConnectionID, ActorID string
}

// AgentTarget names a Book whose built-in agent is on.
type AgentTarget struct {
	TenantID     string `json:"tenant_id"`
	BookID       string `json:"book_id"`
	ConnectionID string `json:"connection_id"`
}

const builtInAgentName = "Built-in agent"

func readAgentSettings(ctx context.Context, tx pgx.Tx, s Scope) (AgentSettings, error) {
	var v AgentSettings
	var level, model *string
	var score, total *int
	var tested *time.Time
	err := tx.QueryRow(ctx, `SELECT a.enabled,a.connection_id::text,a.revision,a.updated_at,
 coalesce((SELECT g.recording_mode FROM public.rule_recording_grants g WHERE g.tenant_id=a.tenant_id AND g.book_id=a.book_id AND g.connection_id=a.connection_id AND g.revoked_at IS NULL),''),
 a.skills_level,a.skills_score,a.skills_total,a.skills_model,a.skills_tested_at,a.default_trust
 FROM public.agent_settings a WHERE a.tenant_id=$1 AND a.book_id=$2`, s.TenantID, s.BookID).Scan(
		&v.Enabled, &v.ConnectionID, &v.Revision, &v.UpdatedAt, &v.RecordingMode, &level, &score, &total, &model, &tested, &v.DefaultTrust)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentSettings{}, ErrNotFound
	}
	if err != nil {
		return v, dbError(err)
	}
	if level != nil && tested != nil {
		v.Skills = &AgentSkills{Level: *level, TestedAt: *tested}
		if score != nil {
			v.Skills.Score = *score
		}
		if total != nil {
			v.Skills.Total = *total
		}
		if model != nil {
			v.Skills.Model = *model
		}
	}
	return v, nil
}

// AgentSettings reads the Book's built-in agent settings for the owner.
func (a TenantAdmin) AgentSettings(ctx context.Context, book string) (AgentSettings, error) {
	var out AgentSettings
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readAgentSettings(ctx, tx, s)
		return err
	})
	return out, err
}

// SaveAgentSettings turns the Book's agent on or off. The first save in a
// Book makes the agent's own proposing connection.
func (a TenantAdmin) SaveAgentSettings(ctx context.Context, book, actor string, q AgentSettingsChange) (AgentSettings, error) {
	var out AgentSettings
	if q.ExpectedRevision < 0 {
		return out, ErrInvalid
	}
	err := a.withRuleOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var revision int64
		var connection string
		err := tx.QueryRow(ctx, "SELECT revision,connection_id::text FROM public.agent_settings WHERE tenant_id=$1 AND book_id=$2 FOR UPDATE", s.TenantID, s.BookID).Scan(&revision, &connection)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return dbError(err)
		}
		if revision != q.ExpectedRevision {
			return ErrConflict
		}
		if revision == 0 {
			connection = ID()
			if _, err := tx.Exec(ctx, `INSERT INTO public.connections(tenant_id,book_id,id,actor_id,name,access) VALUES($1,$2,$3,$4,$5,'propose')`,
				s.TenantID, s.BookID, connection, ID(), builtInAgentName); err != nil {
				return dbError(err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO public.agent_settings(tenant_id,book_id,provider,base_url,model,enabled,connection_id,revision,actor_id)
 VALUES($1,$2,'service','','',$3,$4,1,$5)`, s.TenantID, s.BookID, q.Enabled, connection, actor)
		} else {
			_, err = tx.Exec(ctx, `UPDATE public.agent_settings SET enabled=$3,revision=revision+1,actor_id=$4 WHERE tenant_id=$1 AND book_id=$2 AND revision=$5`,
				s.TenantID, s.BookID, q.Enabled, actor, q.ExpectedRevision)
		}
		if err != nil {
			return bankError(err)
		}
		out, err = readAgentSettings(ctx, tx, s)
		return err
	})
	return out, err
}

// StartProvidedAgent turns on the agent the service provides in a Book
// that has no agent settings yet, with a proposing connection of its own.
// Like every change that gives an agent access, it needs the owner's recent
// sign-in; without one it is refused and tried again when the owner next
// looks. A Book that already has settings keeps them as they are.
func (a TenantAdmin) StartProvidedAgent(ctx context.Context, book, actor string) (AgentSettings, error) {
	var out AgentSettings
	err := a.withAgentNarrowing(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var err error
		if out, err = readAgentSettings(ctx, tx, s); err == nil || !errors.Is(err, ErrNotFound) {
			return err
		}
		connection := ID()
		if _, err := tx.Exec(ctx, `INSERT INTO public.connections(tenant_id,book_id,id,actor_id,name,access) VALUES($1,$2,$3,$4,$5,'propose')`,
			s.TenantID, s.BookID, connection, ID(), builtInAgentName); err != nil {
			return dbError(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.agent_settings(tenant_id,book_id,provider,base_url,model,enabled,connection_id,revision,actor_id)
 VALUES($1,$2,'service','','',true,$3,1,$4)`, s.TenantID, s.BookID, connection, actor); err != nil {
			return bankError(err)
		}
		out, err = readAgentSettings(ctx, tx, s)
		return err
	})
	return out, err
}

// AgentSecret reads whether a Book's agent is on and its connection, for
// the agent's runner.
func (s Store) AgentSecret(ctx context.Context, tenant, book string) (AgentSecret, error) {
	var v AgentSecret
	if s.Runtime == nil || !ValidID(tenant) || !ValidID(book) {
		return v, ErrInvalid
	}
	err := s.Runtime.QueryRow(ctx, "SELECT enabled,connection_id::text,actor_id::text FROM public.agent_secret_for($1,$2)", tenant, book).
		Scan(&v.Enabled, &v.ConnectionID, &v.ActorID)
	return v, dbError(err)
}

// AgentBooks lists Books whose built-in agent is on.
func (s Store) AgentBooks(ctx context.Context) ([]AgentTarget, error) {
	if s.Runtime == nil {
		return nil, ErrDenied
	}
	rows, err := s.Runtime.Query(ctx, "SELECT tenant_id::text,book_id::text,connection_id::text FROM public.agent_books()")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var out []AgentTarget
	for rows.Next() {
		var t AgentTarget
		if err := rows.Scan(&t.TenantID, &t.BookID, &t.ConnectionID); err != nil {
			return nil, dbError(err)
		}
		out = append(out, t)
	}
	return out, dbError(rows.Err())
}
