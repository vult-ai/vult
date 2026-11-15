package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/vult-ai/vult/agentconfig"
	"github.com/vult-ai/vult/ledger"
)

// Settings → Vult's agent: whether Vult's agent is connected to this Vult,
// and whether it is on in a Book. A Vult keeps no model and no model key;
// with no agent connected, the owner signs in to Vult or connects their own.
const agentSettingsPath = "/api/v1/agent-settings"

// OpenAgent prepares the slot for Vult's agent.
func OpenAgent(store ledger.Store) *agentconfig.Service {
	return &agentconfig.Service{Store: store}
}

func (a *webAPI) agentSettings(w http.ResponseWriter, r *http.Request, session string) {
	if a.agent == nil {
		failure(w, http.StatusNotFound)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == agentSettingsPath:
		q := r.URL.Query()
		book := q.Get("book_id")
		if len(q) != 1 {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		tenant, ok := a.bankBook(w, r, session, book)
		if !ok {
			return
		}
		var settings *ledger.AgentSettings
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			v, err := o.AgentSettings(r.Context(), book)
			if err == nil {
				settings = &v
			} else if !errors.Is(err, ledger.ErrNotFound) {
				return err
			}
			return nil
		})
		if bankAnswer(w, err) {
			return
		}
		// A provided agent is on from the start. Starting it is the owner's
		// act and needs a recent sign-in, so it happens here, the first
		// time the owner looks after signing in, for every Book of theirs
		// that has never had agent settings.
		if settings == nil && a.agent.Provided != nil {
			err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
				started, err := o.StartProvidedAgent(r.Context(), book, who)
				if err != nil {
					return err
				}
				settings = &started
				books, err := o.Books(r.Context())
				for _, b := range books {
					if b.ID != book {
						_, _ = o.StartProvidedAgent(r.Context(), b.ID, who)
					}
				}
				return nil
			})
			if err != nil && !errors.Is(err, ledger.ErrFreshOwnerProof) {
				slog.Warn("the provided agent didn't start", "error", err.Error())
			}
		}
		// connected says whether Vult's agent is there to turn on. Without
		// it the page offers signing in to Vult, where this Vult knows a
		// place to, and connecting one's own agent.
		answer := map[string]any{"schema_version": 2, "settings": settings, "connected": a.agent.Provided != nil}
		if a.agent.Provided != nil {
			answer["provided"] = a.agent.Provided
		} else if a.agent.SignInURL != "" {
			answer["sign_in_url"] = a.agent.SignInURL
		}
		_ = json.NewEncoder(w).Encode(answer)
	case r.Method == http.MethodPost && r.URL.Path == agentSettingsPath:
		var q struct {
			BookID string `json:"book_id"`
			agentconfig.Input
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		// Only on or off is taken: a model, an address or a key is refused
		// outright, not quietly dropped.
		decoder.DisallowUnknownFields()
		if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" || decoder.Decode(&q) != nil {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		tenant, ok := a.bankBook(w, r, session, q.BookID)
		if !ok {
			return
		}
		var out ledger.AgentSettings
		err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			var err error
			out, err = a.agent.Save(r.Context(), o, q.BookID, who, q.Input)
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		failure(w, http.StatusNotFound)
	}
}
