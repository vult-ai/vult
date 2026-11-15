package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/version"
	"github.com/vult-ai/vult/ledger"
)

// InstitutionRequests passes an owner's request for a bank Vult can't find
// on to Vult, which counts them to decide which banks to support next. Every
// Vult sends them there by default, only when the person clicks; an operator
// can send them elsewhere or turn them off.
type InstitutionRequests interface {
	// Notify is how the person can hear back: "account" when the receiver
	// knows the account, "email" when they type one, "none" when it can't
	// tell anyone and the screen offers no such option.
	Notify() string
	Send(ctx context.Context, r InstitutionRequest) error
}

// InstitutionRequest is one request from the screen. A request exists only
// when search found nothing, so there's no provider id.
type InstitutionRequest struct {
	Key     string // de-duplicates clicks on this Vult; never sent
	Name    string // what the person searched for
	Domain  string // the bank's website, a public domain that resolves
	Country string
	Email   string // only when the person asked to hear back
	Version string
	Edition string
	// Notify is whether the person asked to hear back; a receiver that
	// knows the account uses it in place of an email.
	Notify bool
	// Subject identifies the signed-in owner to a receiver that notifies by
	// account; the forwarder below never sends it.
	Subject string
}

const institutionRequestsPath = "/api/v1/bank/institution-requests"

// offerBankRequests says whether the owner can ask Vult to add a bank it
// can't find. It's off: nothing is sent anywhere, no installation id or
// email leaves this Vult, and the request route isn't there. The code
// stays for when Vult takes requests.
var offerBankRequests = false

// DefaultInstitutionRequestsURL is Vult's own address for bank requests,
// used when the operator configures none.
const DefaultInstitutionRequestsURL = "https://vult.ai/api/v1/bank-requests"

// institutionRequestsURL checks the configured address requests go to:
// empty for Vult's own, "off" for none, otherwise one HTTPS URL.
func institutionRequestsURL(raw string) (string, error) {
	if raw == "" {
		raw = DefaultInstitutionRequestsURL
	}
	if raw == "" || raw == "off" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("institution_requests must be an HTTPS URL or off")
	}
	return u.String(), nil
}

// vultID names this Vult to the receiver so it counts one request per Vult:
// 16 to 64 letters, digits, - and _, random and from nothing personal.
var vultID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// chooseRequests is the receiver for this server: none when configured
// "off", the host's own when it supplies one, or else the configured
// address or Vult's own. id is the installation's vult_id; without one, a
// random one stands for this run.
func chooseRequests(configured string, given InstitutionRequests, id string) (InstitutionRequests, error) {
	address, err := institutionRequestsURL(configured)
	switch {
	case err != nil:
		return nil, err
	case configured == "off":
		return nil, nil
	case given != nil:
		return given, nil
	case address != "":
		if id == "" {
			id = strings.ReplaceAll(ledger.ID(), "-", "")
		}
		if !vultID.MatchString(id) {
			return nil, errors.New("vult_id must be 16 to 64 letters, digits, - or _")
		}
		return forwardedRequests{url: address, id: id, client: &http.Client{Timeout: 15 * time.Second}}, nil
	}
	return nil, nil
}

// forwardedRequests posts requests to Vult's address (or the operator's),
// as Vult Open. The person may give an email to hear when the bank is
// added; it's sent only then.
type forwardedRequests struct {
	url    string
	id     string
	client *http.Client
}

func (f forwardedRequests) Notify() string { return "email" }

// errRequestsUnavailable means the receiver couldn't be reached or can't
// answer now; the same request can be sent again.
var errRequestsUnavailable = errors.New("Vult couldn't be reached. Try again in a little while.")

// limitedError is the receiver asking this Vult to wait before asking for
// more banks.
type limitedError struct{ retryAfter string }

func (limitedError) Error() string {
	return "Vult has had many requests from here lately. Try again later."
}

func (f forwardedRequests) Send(ctx context.Context, r InstitutionRequest) error {
	wire := map[string]string{"name": r.Name, "domain": r.Domain, "vult_version": r.Version, "edition": "open", "vult_id": f.id}
	if r.Country != "" {
		wire["country"] = r.Country
	}
	if r.Notify && r.Email != "" {
		wire["notify_email"] = r.Email
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := f.client.Do(req)
	if err != nil {
		return errRequestsUnavailable
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	switch res.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusBadRequest:
		var refused struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &refused) == nil && refused.Message != "" && len(refused.Message) <= 300 {
			return &ledger.RefusedError{Reason: refused.Message}
		}
		return &ledger.RefusedError{Reason: "Vult couldn't take this request. Check the bank's name and website."}
	case http.StatusTooManyRequests:
		after := res.Header.Get("Retry-After")
		if _, err := strconv.Atoi(after); err != nil {
			after = "3600"
		}
		return limitedError{retryAfter: after}
	}
	return errRequestsUnavailable
}

// sentRequests remembers requests already sent for a day, by their key and
// by Book and bank name, so a second click never sends twice.
type sentRequests struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (s *sentRequests) claim(keys ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.seen == nil || len(s.seen) > 4096 {
		s.seen = map[string]time.Time{}
	}
	for _, k := range keys {
		if at, ok := s.seen[k]; ok && now.Sub(at) < 24*time.Hour {
			return false
		}
	}
	for _, k := range keys {
		s.seen[k] = now
	}
	return true
}

func (s *sentRequests) release(keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.seen, k)
	}
}

var requestKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func plainName(s string) bool {
	if s == "" || len(s) > 120 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func plainEmail(s string) bool {
	at := strings.LastIndex(s, "@")
	return len(s) <= 254 && at > 0 && at < len(s)-3 && strings.Contains(s[at:], ".") && !strings.ContainsAny(s, " \t\r\n<>,;")
}

// oneCountry reports whether s is one two-letter country code, upper case.
func oneCountry(s string) bool {
	list, ok := parseCountries(s)
	return ok && len(list) == 1 && list[0] == s
}

// requestsOffer is what the picker is told about requests: nil when they
// are off.
func (a *webAPI) requestsOffer() map[string]string {
	if a.bankRequests == nil {
		return nil
	}
	return map[string]string{"notify": a.bankRequests.Notify()}
}

func (a *webAPI) institutionRequest(w http.ResponseWriter, r *http.Request, session string) {
	if a.bankRequests == nil {
		failure(w, http.StatusNotFound)
		return
	}
	var q struct {
		BookID     string `json:"book_id"`
		RequestKey string `json:"request_key"`
		Name       string `json:"name"`
		Website    string `json:"website"`
		Country    string `json:"country,omitempty"`
		Notify     bool   `json:"notify,omitempty"`
		Email      string `json:"email,omitempty"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" || err != nil || strictjson.Decode(body, &q) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	email := strings.TrimSpace(q.Email)
	mode := a.bankRequests.Notify()
	if !requestKey.MatchString(q.RequestKey) || !plainName(q.Name) || q.Country != "" && !oneCountry(q.Country) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	website, ok := websiteDomain(q.Website)
	if !ok {
		apiFailure(w, &ledger.RefusedError{Reason: "Enter the bank's website, such as examplebank.com."})
		return
	}
	if !q.Notify || mode != "email" {
		// No email goes anywhere unless the person asked to hear back and
		// the receiver takes one; a receiver that knows the account never
		// needs one typed.
		email = ""
		q.Notify = q.Notify && mode == "account"
	} else if !plainEmail(email) {
		apiFailure(w, &ledger.RefusedError{Reason: "Enter the email to tell when the bank is added."})
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	id, err := a.sessions.Identity(r.Context(), session)
	if err != nil {
		failure(w, http.StatusUnauthorized)
		return
	}
	// The website must be a real one before anything is sent.
	if a.resolveHost(r.Context(), website) != nil {
		apiFailure(w, &ledger.RefusedError{Reason: website + " couldn't be found. Check the bank's website and try again."})
		return
	}
	keys := []string{"key/" + q.RequestKey, "name/" + tenant + "/" + q.BookID + "/" + strings.ToLower(strings.Join(strings.Fields(q.Name), " ")) + "/" + time.Now().UTC().Format("2006-01-02")}
	if a.requestsSent.claim(keys...) {
		name := strings.Join(strings.Fields(q.Name), " ")
		err := a.bankRequests.Send(r.Context(), InstitutionRequest{Key: q.RequestKey, Name: name, Domain: website, Country: q.Country,
			Notify: q.Notify, Email: email, Version: version.Version, Edition: "open", Subject: id.Subject})
		if err == nil {
			// What left this Vult, for the operator's own record.
			slog.Info("bank request sent", "request_key", q.RequestKey, "name", name, "domain", website, "country", q.Country, "email_included", email != "")
		}
		if err != nil {
			a.requestsSent.release(keys...)
			var refused *ledger.RefusedError
			var limited limitedError
			switch {
			case errors.As(err, &refused):
				apiFailure(w, err)
			case errors.As(err, &limited):
				w.Header().Set("Retry-After", limited.retryAfter)
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "error": "limited", "message": limited.Error()})
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "error": "unavailable", "message": errRequestsUnavailable.Error()})
			}
			return
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "requested"})
}

// websiteDomain reads the bank's website as a public domain name, from
// "examplebank.com", "www.examplebank.com" or a full address.
func websiteDomain(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 253 {
		return "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Port() != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	labels := strings.Split(host, ".")
	if len(labels) < 2 || net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return "", false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	if tld := labels[len(labels)-1]; len(tld) < 2 || tld[0] >= '0' && tld[0] <= '9' {
		return "", false
	}
	return host, true
}

// lookupHost checks a domain resolves, within a few seconds.
func lookupHost(ctx context.Context, host string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err == nil && len(addrs) == 0 {
		err = errors.New("no addresses")
	}
	return err
}

func (a *webAPI) resolveHost(ctx context.Context, host string) error {
	if a.lookupHost != nil {
		return a.lookupHost(ctx, host)
	}
	return lookupHost(ctx, host)
}
