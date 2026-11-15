package bankconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vult-ai/vult/ledger"
)

// Mercury reads a Mercury business account with a read-only API token the
// owner creates in Mercury and pastes into Vult (TokenConnector). It needs
// no provider keys and opens no provider screen.
//
// Mercury's API has no change cursor for transactions, so a sync rereads a
// window of dates: from the oldest transaction that was still pending, or a
// few days before the last sync, whichever is earlier. Every transaction in
// the window is reported again; the inbox keeps one item per transaction.
//
// Every sync checks each account's activity against the balance Mercury
// reports. An account whose numbers don't add up has none of its activity
// reported, so a misread sign or a missed transaction never reaches the
// Book.
type Mercury struct {
	// Client makes the HTTPS calls; nil uses a client with a timeout.
	Client *http.Client
	// BaseURL overrides the API root, for tests.
	BaseURL string
	// Now is the clock, for tests; nil uses the system clock.
	Now func() time.Time
}

const (
	mercuryAPI = "https://api.mercury.com/api/v1"
	// mercuryHistoryDays is how far back the first sync reports activity.
	// It reads the account's whole history to check its balance.
	mercuryHistoryDays = 730
	// mercuryOverlapDays is how far before the last sync the next one starts
	// reading, so transactions Mercury reports late are still picked up.
	mercuryOverlapDays = 7
	mercuryPageSize    = 1000
	mercuryMaxPages    = 500
	mercuryCursorV2    = "mercury-v2"
	mercuryInstitution = "Mercury"
	mercuryCursorLimit = 4096
)

var errMercuryNotFound = errors.New("mercury: not found")

func (m *Mercury) Provider() string { return "mercury" }

func (m *Mercury) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (m *Mercury) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// StartLink is unsupported: the owner pastes a token instead.
func (m *Mercury) StartLink(context.Context, Credentials, string, string) (LinkStart, error) {
	return LinkStart{}, ErrUnsupported
}

// FinishLink is unsupported: the owner pastes a token instead.
func (m *Mercury) FinishLink(context.Context, Credentials, string) (Linked, error) {
	return Linked{}, ErrUnsupported
}

// Remove has nothing to ask of Mercury: its API can't revoke a token. The
// owner deletes the token in Mercury; Vult forgets its sealed copy.
func (m *Mercury) Remove(context.Context, Credentials, string) error { return nil }

// ConnectToken checks the pasted token by reading the accounts it can see.
func (m *Mercury) ConnectToken(ctx context.Context, token string) (Linked, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Linked{}, ErrCredentials
	}
	accounts, err := m.accounts(ctx, m.tokenAuth(token))
	if errors.Is(err, ErrReconnect) {
		return Linked{}, ErrCredentials
	}
	if err != nil {
		return Linked{}, err
	}
	first, ok := mercuryFirstOpen(accounts)
	if !ok {
		return Linked{}, fmt.Errorf("%w: this token can't see a checking, savings or credit account", ErrUnsupported)
	}
	return Linked{AccessToken: token, ItemID: "mercury:" + first,
		InstitutionID: "mercury", InstitutionName: mercuryInstitution}, nil
}

// mercuryAuth is how one sync reaches Mercury's API: the API root and the
// Authorization header.
type mercuryAuth struct {
	base, header string
}

// tokenAuth reaches the production API with a token the owner pasted.
// Mercury's tokens start with "secret-token:"; owners may paste them
// without it.
func (m *Mercury) tokenAuth(token string) mercuryAuth {
	token = strings.TrimSpace(token)
	if token == "" {
		return mercuryAuth{base: m.apiBase()}
	}
	if !strings.HasPrefix(token, "secret-token:") {
		token = "secret-token:" + token
	}
	return mercuryAuth{base: m.apiBase(), header: "Bearer " + token}
}

func (m *Mercury) apiBase() string {
	if m.BaseURL != "" {
		return m.BaseURL
	}
	return mercuryAPI
}

// get reads one Mercury endpoint and decodes its reply.
func (m *Mercury) get(ctx context.Context, auth mercuryAuth, path string, query url.Values, out any) error {
	if auth.header == "" {
		return ErrReconnect
	}
	target := auth.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth.header)
	req.Header.Set("Accept", "application/json")
	res, err := m.client().Do(req)
	if err != nil {
		return ErrProvider
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return ErrProvider
	}
	switch {
	case res.StatusCode == http.StatusOK:
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		// A token the owner deleted, or one Mercury removed after going
		// unused.
		return ErrReconnect
	case res.StatusCode == http.StatusNotFound:
		return errMercuryNotFound
	case res.StatusCode == http.StatusBadRequest:
		return ErrUnsupported
	default:
		return ErrProvider
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return ErrProvider
	}
	return nil
}

type mercuryAccount struct {
	ID             string      `json:"id"`
	AccountNumber  string      `json:"accountNumber"`
	RoutingNumber  string      `json:"routingNumber"`
	Name           string      `json:"name"`
	Nickname       string      `json:"nickname"`
	Status         string      `json:"status"`
	Type           string      `json:"type"`
	Kind           string      `json:"kind"`
	CreatedAt      string      `json:"createdAt"`
	CurrentBalance json.Number `json:"currentBalance"`
}

type mercuryTransaction struct {
	ID               string      `json:"id"`
	Amount           json.Number `json:"amount"`
	Status           string      `json:"status"`
	Kind             string      `json:"kind"`
	CreatedAt        string      `json:"createdAt"`
	PostedAt         string      `json:"postedAt"`
	CounterpartyName string      `json:"counterpartyName"`
	BankDescription  string      `json:"bankDescription"`
	Note             string      `json:"note"`
}

// mercuryHeld is one account as Mercury reports it, with what the balance
// check needs.
type mercuryHeld struct {
	Account
	created time.Time
	// balance is Mercury's current balance in minor units, in Mercury's own
	// sign.
	balance int64
	// closed is an account Mercury archived or deleted.
	closed  bool
	routing string
}

// accounts lists the deposit and credit accounts the token can see.
func (m *Mercury) accounts(ctx context.Context, token mercuryAuth) ([]mercuryHeld, error) {
	scale, _ := ledger.CurrencyScale("USD")
	var out []mercuryHeld
	after := ""
	for page := 0; ; page++ {
		if page >= mercuryMaxPages {
			return nil, ErrProvider
		}
		q := url.Values{"limit": {strconv.Itoa(mercuryPageSize)}, "order": {"asc"}}
		if after != "" {
			q.Set("start_after", after)
		}
		var r struct {
			Accounts []mercuryAccount `json:"accounts"`
			Page     struct {
				NextPage string `json:"nextPage"`
			} `json:"page"`
		}
		if err := m.get(ctx, token, "/accounts", q, &r); err != nil {
			return nil, err
		}
		for _, a := range r.Accounts {
			if v, closed, ok := mercuryDepositOf(a, scale); ok {
				held, err := mercuryHeldOf(v, a.CreatedAt, a.CurrentBalance)
				if err != nil {
					return nil, err
				}
				held.closed, held.routing = closed, a.RoutingNumber
				out = append(out, held)
			}
		}
		if r.Page.NextPage == "" || len(r.Accounts) == 0 {
			break
		}
		after = r.Accounts[len(r.Accounts)-1].ID
	}
	var credit struct {
		Accounts []struct {
			ID             string      `json:"id"`
			Status         string      `json:"status"`
			CreatedAt      string      `json:"createdAt"`
			CurrentBalance json.Number `json:"currentBalance"`
		} `json:"accounts"`
	}
	// Credit is only offered to some businesses; a token that can't see it
	// still reads the deposit accounts.
	switch err := m.get(ctx, token, "/credit", nil, &credit); {
	case err == nil:
		for _, a := range credit.Accounts {
			closed, ok := mercuryStatus(a.Status)
			if a.ID == "" || !ok {
				continue
			}
			held, err := mercuryHeldOf(Account{ProviderAccountID: a.ID, Name: "Mercury Credit", Type: "credit", Subtype: "credit card",
				Kind: "card", Currency: "USD", Scale: scale}, a.CreatedAt, a.CurrentBalance)
			if err != nil {
				return nil, err
			}
			held.closed = closed
			out = append(out, held)
		}
	case errors.Is(err, errMercuryNotFound), errors.Is(err, ErrReconnect) && len(out) > 0:
	default:
		return nil, err
	}
	return out, nil
}

func mercuryHeldOf(a Account, created string, balance json.Number) (mercuryHeld, error) {
	day, ok := mercuryDay(created)
	if !ok {
		return mercuryHeld{}, fmt.Errorf("%w: an account without an opening date", ErrUnsupported)
	}
	minor, err := minorUnits(balance.String(), a.Scale)
	if err != nil {
		return mercuryHeld{}, err
	}
	return mercuryHeld{Account: a, created: day, balance: minor}, nil
}

// mercuryStatus reads an account's status: open, or closed (archived or
// deleted). An account still being opened is left out until it is active.
func mercuryStatus(status string) (closed, ok bool) {
	switch strings.ToLower(status) {
	case "active":
		return false, true
	case "archived", "deleted":
		return true, true
	}
	return false, false
}

// mercuryDepositOf keeps the business's own checking and savings accounts,
// open or closed. Treasury and investment accounts are left out.
func mercuryDepositOf(a mercuryAccount, scale int) (Account, bool, bool) {
	kind := strings.ToLower(a.Kind)
	subtype := ""
	switch {
	case strings.Contains(kind, "checking"):
		subtype = "checking"
	case strings.Contains(kind, "savings"):
		subtype = "savings"
	}
	closed, ok := mercuryStatus(a.Status)
	if a.ID == "" || subtype == "" || !ok || (a.Type != "" && !strings.EqualFold(a.Type, "mercury")) {
		return Account{}, false, false
	}
	name := a.Nickname
	if name == "" {
		name = a.Name
	}
	mask := a.AccountNumber
	if len(mask) > 4 {
		mask = mask[len(mask)-4:]
	}
	return Account{ProviderAccountID: a.ID, Name: name, Mask: mask, Type: "depository", Subtype: subtype,
		Kind: "cash", Currency: "USD", Scale: scale}, closed, true
}

// mercuryBase is what one account's last sync that added up leaves for the
// next: the day the next sync reads from, and the balance before every
// posted transaction created since that day. Mercury doesn't say whether its
// current balance counts pending transactions, so both are kept: without
// them (posted) and with them (all).
type mercuryBase struct {
	from        time.Time
	posted, all int64
	// balance is Mercury's balance when the base was made.
	balance int64
}

// mercuryCursor is the day of the sync that wrote it and each account's
// base. An account without a base reads its whole history next time.
type mercuryCursor struct {
	synced   time.Time
	accounts map[string]mercuryBase
}

func (c mercuryCursor) String() string {
	var b strings.Builder
	b.WriteString(mercuryCursorV2 + ";" + c.synced.Format(time.DateOnly))
	ids := make([]string, 0, len(c.accounts))
	for id := range c.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		v := c.accounts[id]
		fmt.Fprintf(&b, ";%s=%s,%d,%d,%d", id, v.from.Format(time.DateOnly), v.posted, v.all, v.balance)
	}
	return b.String()
}

func parseMercuryCursor(text string) (mercuryCursor, error) {
	bad := fmt.Errorf("%w: cursor %q", ErrUnsupported, text)
	parts := strings.Split(text, ";")
	if len(parts) < 2 || parts[0] != mercuryCursorV2 {
		return mercuryCursor{}, bad
	}
	synced, err := time.Parse(time.DateOnly, parts[1])
	if err != nil {
		return mercuryCursor{}, bad
	}
	out := mercuryCursor{synced: synced, accounts: map[string]mercuryBase{}}
	for _, p := range parts[2:] {
		id, rest, ok := strings.Cut(p, "=")
		fields := strings.Split(rest, ",")
		if !ok || id == "" || len(fields) != 4 {
			return mercuryCursor{}, bad
		}
		from, err1 := time.Parse(time.DateOnly, fields[0])
		posted, err2 := strconv.ParseInt(fields[1], 10, 64)
		all, err3 := strconv.ParseInt(fields[2], 10, 64)
		balance, err4 := strconv.ParseInt(fields[3], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return mercuryCursor{}, bad
		}
		out.accounts[id] = mercuryBase{from: from, posted: posted, all: all, balance: balance}
	}
	return out, nil
}

// Changes reads every account's transactions since cursor. Mercury keeps
// a transaction's ID when it posts, so a pending transaction that posts is
// reported again under the same ID with Pending false, and one Mercury
// cancels, fails, blocks or reverses is reported as removed.
//
// When Mercury opens new accounts to take over old ones, as it does when it
// moves its customers to a new partner bank, the new account is proposed as
// the old one's replacement. The two are checked together if either doesn't
// add up alone: Mercury may move the money with a transfer both accounts
// show, or by carrying the balance over with no transaction at all. A
// closed account is synced one last time and then left out.
func (m *Mercury) Changes(ctx context.Context, _ Credentials, access, cursor string) (Changes, error) {
	token := m.tokenAuth(access)
	var err error
	today := m.now().UTC().Truncate(24 * time.Hour)
	prev := mercuryCursor{accounts: map[string]mercuryBase{}}
	if cursor != "" {
		if prev, err = parseMercuryCursor(cursor); err != nil {
			return Changes{}, err
		}
	}
	accounts, err := m.accounts(ctx, token)
	if err != nil {
		return Changes{}, err
	}
	var runs []*mercuryRun
	listed := map[string]bool{}
	for _, a := range accounts {
		listed[a.ProviderAccountID] = true
		base, known := prev.accounts[a.ProviderAccountID]
		if a.closed && !known {
			// An account closed before Vult first saw it has nothing to add.
			continue
		}
		if !known {
			// The first sync of an account reads its whole history, so its
			// balance can be checked from zero.
			base = mercuryBase{from: a.created}
		}
		run := &mercuryRun{held: a, base: base, known: known}
		list, err := m.transactions(ctx, token, a.ProviderAccountID, base.from, today)
		switch {
		case errors.Is(err, errMercuryNotFound) && a.closed:
			// Mercury no longer shows a closed account's transactions.
			run.unreadable = true
		case errors.Is(err, errMercuryNotFound) && a.Kind == "card" && !known:
			// The credit account's transactions aren't readable here; leave
			// the account out rather than show it empty.
			continue
		case err != nil:
			return Changes{}, err
		default:
			if run.read, err = mercuryRead(list, a, base, today, known, prev.synced); err != nil {
				return Changes{}, err
			}
			run.passed = run.read.addsUp(a.balance, base)
		}
		runs = append(runs, run)
	}
	for id, base := range prev.accounts {
		if !listed[id] {
			// An account Mercury no longer lists at all is closed, with
			// nothing more to read.
			runs = append(runs, &mercuryRun{held: mercuryHeld{Account: Account{ProviderAccountID: id}, closed: true}, base: base, known: true, unreadable: true, vanished: true})
		}
	}
	replacements := mercuryReplacements(runs, len(prev.accounts) > 0)
	for _, p := range replacements {
		mercuryCheckTogether(p.old, p.new)
	}

	out := Changes{Added: []Transaction{}, Modified: []Transaction{}}
	next := mercuryCursor{synced: today, accounts: map[string]mercuryBase{}}
	for _, p := range replacements {
		out.Replacements = append(out.Replacements, Replacement{OldProviderAccountID: p.old.held.ProviderAccountID,
			NewProviderAccountID: p.new.held.ProviderAccountID, Reason: p.reason()})
	}
	for _, run := range runs {
		a := run.held
		if run.unreadable {
			// A closed account whose last activity can't be read is fine
			// when it was empty at the last sync, or when its replacement
			// accounts for what it held.
			if !run.passed && run.base.balance != 0 {
				problem := "Mercury closed " + a.Name + " before Vult could read its last activity — check its final statement."
				if run.vanished {
					problem = "A Mercury account Vult was syncing is gone before Vult could read its last activity — check its final statement."
				}
				out.Checks = append(out.Checks, BalanceCheck{ProviderAccountID: a.ProviderAccountID, Name: run.label(), Problem: problem})
			}
			out.Closed = append(out.Closed, a.ProviderAccountID)
			continue
		}
		out.Accounts = append(out.Accounts, a.Account)
		check := BalanceCheck{ProviderAccountID: a.ProviderAccountID, Name: a.Name, BalanceMinor: a.balance,
			Currency: a.Currency, Scale: a.Scale, Passed: run.passed}
		if a.Kind == "card" {
			// Mercury signs what is owed as negative; Vult shows it as owed.
			check.BalanceMinor = -a.balance
		}
		if !run.passed {
			check.Problem = "Mercury's numbers don't add up for " + a.Name + " — nothing was recorded."
			if run.known {
				// The last base that added up stays until one does again.
				next.accounts[a.ProviderAccountID] = run.base
			}
			out.Checks = append(out.Checks, check)
			continue
		}
		r := run.read
		check.Transactions = len(r.added) + len(r.modified)
		out.Checks = append(out.Checks, check)
		out.Added = append(out.Added, r.added...)
		out.Modified = append(out.Modified, r.modified...)
		out.Removed = append(out.Removed, r.removed...)
		if a.closed {
			out.Closed = append(out.Closed, a.ProviderAccountID)
			continue
		}
		next.accounts[a.ProviderAccountID] = r.nextBase(a.balance, run.base)
	}
	// A transaction that posted while the sync was reading would make a
	// balance look wrong; read the balances again and retry later if any
	// moved.
	again, err := m.accounts(ctx, token)
	if err != nil {
		return Changes{}, err
	}
	moved := map[string]int64{}
	for _, a := range accounts {
		moved[a.ProviderAccountID] = a.balance
	}
	for _, a := range again {
		if b, ok := moved[a.ProviderAccountID]; ok && b != a.balance {
			return Changes{}, fmt.Errorf("%w: a balance changed during the sync", ErrProvider)
		}
	}
	out.NextCursor = next.String()
	if len(out.NextCursor) > mercuryCursorLimit {
		return Changes{}, fmt.Errorf("%w: too many accounts for one connection", ErrUnsupported)
	}
	return out, nil
}

func mercuryFirstOpen(accounts []mercuryHeld) (string, bool) {
	for _, a := range accounts {
		if !a.closed {
			return a.ProviderAccountID, true
		}
	}
	return "", false
}

// mercuryRun is one account in one sync.
type mercuryRun struct {
	held  mercuryHeld
	base  mercuryBase
	known bool
	read  mercuryResult
	// passed is whether the account adds up, alone or with its pair.
	passed bool
	// unreadable is a closed account whose transactions can't be read;
	// vanished is one Mercury no longer lists at all.
	unreadable, vanished bool
}

func (r *mercuryRun) label() string {
	if r.held.Name != "" {
		return r.held.Name
	}
	return "Mercury account"
}

type mercuryPair struct{ old, new *mercuryRun }

func (p mercuryPair) reason() string {
	n := p.new.held
	text := "Mercury opened " + n.Name
	if n.Mask != "" {
		text += " ••" + n.Mask
	}
	text += " to take over " + p.old.label()
	if p.old.held.Mask != "" {
		text += " ••" + p.old.held.Mask
	}
	return text + ", as it does when it moves accounts to a new partner bank. Confirm to keep recording it to the same Book account."
}

// mercuryReplacements pairs each account seen for the first time with the
// known account it takes over: one of the same kind that Mercury closed, or
// that sits at a different bank (routing number) from the new one. When a
// kind has several candidates, only pairs with the same name are made, and
// only when the pairing is unambiguous. The first sync of a connection has
// nothing to take over.
func mercuryReplacements(runs []*mercuryRun, anyKnown bool) []mercuryPair {
	if !anyKnown {
		return nil
	}
	type group struct{ olds, news []*mercuryRun }
	groups := map[string]*group{}
	key := func(a mercuryHeld) string { return a.Kind + "/" + a.Subtype }
	for _, r := range runs {
		if !r.known && !r.held.closed {
			g := groups[key(r.held)]
			if g == nil {
				g = &group{}
				groups[key(r.held)] = g
			}
			g.news = append(g.news, r)
		}
	}
	for _, r := range runs {
		g := groups[key(r.held)]
		if !r.known || g == nil {
			continue
		}
		moved := r.held.closed
		for _, n := range g.news {
			moved = moved || (r.held.routing != "" && n.held.routing != "" && r.held.routing != n.held.routing)
		}
		if moved && !r.vanished {
			g.olds = append(g.olds, r)
		}
	}
	var out []mercuryPair
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		if len(g.olds) == 1 && len(g.news) == 1 {
			out = append(out, mercuryPair{old: g.olds[0], new: g.news[0]})
			continue
		}
		byName := func(list []*mercuryRun) map[string][]*mercuryRun {
			m := map[string][]*mercuryRun{}
			for _, r := range list {
				name := strings.ToLower(strings.TrimSpace(r.held.Name))
				m[name] = append(m[name], r)
			}
			return m
		}
		olds, news := byName(g.olds), byName(g.news)
		for name, o := range olds {
			if n := news[name]; len(o) == 1 && len(n) == 1 {
				out = append(out, mercuryPair{old: o[0], new: n[0]})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].new.held.ProviderAccountID < out[j].new.held.ProviderAccountID })
	return out
}

// mercuryCheckTogether checks a replacement pair as one account when either
// side doesn't add up alone: the old account's last base, plus everything
// both accounts show since, must equal their two balances together. That
// holds whether Mercury moved the money with a transfer both show or
// carried the balance over silently. A closed old account whose
// transactions can't be read is taken at its last balance.
func mercuryCheckTogether(old, new *mercuryRun) {
	if old.passed && new.passed {
		return
	}
	if !new.read.signsAgree {
		return
	}
	if old.unreadable {
		if new.held.balance == old.base.balance+new.read.postedSum || new.held.balance == old.base.balance+new.read.postedSum+new.read.pendingSum {
			old.passed, new.passed = true, true
		}
		return
	}
	if !old.read.signsAgree {
		return
	}
	balances := old.held.balance + new.held.balance
	posted := old.base.posted + old.read.postedSum + new.read.postedSum
	all := old.base.all + old.read.postedSum + old.read.pendingSum + new.read.postedSum + new.read.pendingSum
	if balances == posted || balances == all {
		old.passed, new.passed = true, true
	}
}

// mercuryResult is one account's activity from one sync, with the sums the
// balance check needs.
type mercuryResult struct {
	added, modified []Transaction
	removed         []string
	// signsAgree is false when a card purchase put money in or a refund
	// took money out: Mercury's signs aren't the ones Vult reads.
	signsAgree            bool
	postedSum, pendingSum int64
	posted                []mercuryPosting
	nextFrom              time.Time
}

type mercuryPosting struct {
	created time.Time
	amount  int64
}

// addsUp checks the account alone. The balance must equal the base plus
// every posted transaction created since the base's day, with or without
// the pending ones, and card purchases must take money out and refunds put
// it in, which settles which way Mercury signs amounts.
func (r mercuryResult) addsUp(balance int64, base mercuryBase) bool {
	return r.signsAgree && (balance == base.posted+r.postedSum || balance == base.all+r.postedSum+r.pendingSum)
}

// nextBase is the base the next sync starts from.
func (r mercuryResult) nextBase(balance int64, base mercuryBase) mercuryBase {
	from := r.nextFrom
	if from.Before(base.from) {
		from = base.from
	}
	out := mercuryBase{from: from, posted: balance, all: balance - r.pendingSum, balance: balance}
	for _, p := range r.posted {
		if !p.created.Before(from) {
			out.posted -= p.amount
			out.all -= p.amount
		}
	}
	return out
}

// mercuryRead turns one account's transactions into activity and the sums
// its balance check needs.
func mercuryRead(list []mercuryTransaction, a mercuryHeld, base mercuryBase, today time.Time, known bool, synced time.Time) (mercuryResult, error) {
	out := mercuryResult{signsAgree: true}
	reportFrom := today.AddDate(0, 0, -mercuryHistoryDays)
	out.nextFrom = today.AddDate(0, 0, -mercuryOverlapDays)
	for _, t := range list {
		status := strings.ToLower(t.Status)
		switch status {
		case "cancelled", "failed", "blocked", "reversed":
			out.removed = append(out.removed, t.ID)
			continue
		case "pending", "sent":
		default:
			// A status Vult doesn't know yet is read again next time. If it
			// moved the balance, the check fails and nothing is recorded.
			if created, ok := mercuryDay(t.CreatedAt); ok && created.Before(out.nextFrom) {
				out.nextFrom = created
			}
			continue
		}
		v, created, err := mercuryTransactionOf(t, a.Account)
		if err != nil {
			return out, err
		}
		raw, err := minorUnits(t.Amount.String(), a.Scale)
		if err != nil {
			return out, err
		}
		switch t.Kind {
		case "creditCardTransaction", "debitCardTransaction":
			out.signsAgree = out.signsAgree && raw <= 0
		case "creditCardCredit", "debitCardCredit":
			out.signsAgree = out.signsAgree && raw >= 0
		}
		if v.Pending {
			out.pendingSum += raw
			if created.Before(out.nextFrom) {
				out.nextFrom = created
			}
		} else {
			// Sums go by the day a transaction was created, the same day the
			// window is read by, so one that posts later is counted once.
			if !created.Before(base.from) {
				out.postedSum += raw
			}
			out.posted = append(out.posted, mercuryPosting{created: created, amount: raw})
		}
		if v.AmountMinor == 0 {
			continue
		}
		switch {
		case !known && created.Before(reportFrom):
			// Read for the balance check, older than Vult imports.
		case !known || !created.Before(synced):
			out.added = append(out.added, v)
		default:
			out.modified = append(out.modified, v)
		}
	}
	return out, nil
}

// transactions reads one account's transactions created between from and
// to, page by page.
func (m *Mercury) transactions(ctx context.Context, token mercuryAuth, account string, from, to time.Time) ([]mercuryTransaction, error) {
	var out []mercuryTransaction
	for page := 0; page < mercuryMaxPages; page++ {
		q := url.Values{"limit": {strconv.Itoa(mercuryPageSize)}, "offset": {strconv.Itoa(len(out))}, "order": {"asc"},
			// The end date is a day late so nothing from today is cut off by
			// the difference between Mercury's day and UTC.
			"start": {from.Format(time.DateOnly)}, "end": {to.AddDate(0, 0, 1).Format(time.DateOnly)}}
		var r struct {
			Total        int                  `json:"total"`
			Transactions []mercuryTransaction `json:"transactions"`
		}
		if err := m.get(ctx, token, "/account/"+url.PathEscape(account)+"/transactions", q, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Transactions...)
		if len(r.Transactions) == 0 || len(out) >= r.Total {
			return out, nil
		}
	}
	return nil, ErrProvider
}

func mercuryDay(stamp string) (time.Time, bool) {
	if stamp == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		return t.UTC().Truncate(24 * time.Hour), true
	}
	if t, err := time.Parse(time.DateOnly, stamp[:min(len(stamp), 10)]); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// mercuryTransactionOf signs Mercury's amount, where negative is money
// leaving the business, the way the account's balance moves: a purchase
// lowers a cash balance and raises what is owed on a card. The balance
// check confirms the sign before anything is reported.
func mercuryTransactionOf(t mercuryTransaction, a Account) (Transaction, time.Time, error) {
	created, ok := mercuryDay(t.CreatedAt)
	if !ok || t.ID == "" {
		return Transaction{}, time.Time{}, fmt.Errorf("%w: a transaction without an ID or date", ErrUnsupported)
	}
	minor, err := minorUnits(t.Amount.String(), a.Scale)
	if err != nil {
		return Transaction{}, time.Time{}, err
	}
	if a.Kind == "card" {
		minor = -minor
	}
	pending := strings.EqualFold(t.Status, "pending")
	date := created
	if posted, ok := mercuryDay(t.PostedAt); ok && !pending {
		date = posted
	}
	description := t.BankDescription
	if description == "" {
		description = t.CounterpartyName
	}
	if description == "" {
		description = t.Note
	}
	return Transaction{ProviderAccountID: a.ProviderAccountID, ProviderTransactionID: t.ID,
		Date: date.Format(time.DateOnly), AuthorizedDate: created.Format(time.DateOnly),
		AmountMinor: minor, Currency: a.Currency, Scale: a.Scale, Description: description,
		Counterparty: t.CounterpartyName, Pending: pending}, created, nil
}

// minorUnits converts a decimal amount to exact minor units. Extra decimal
// places are accepted only when they are zero: Vult never rounds money.
func minorUnits(text string, scale int) (int64, error) {
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	whole, frac, _ := strings.Cut(text, ".")
	if whole == "" || strings.ContainsAny(whole+frac, "eE+-") {
		return 0, fmt.Errorf("%w: amount %q", ErrUnsupported, text)
	}
	if len(frac) > scale {
		if strings.Trim(frac[scale:], "0") != "" {
			return 0, fmt.Errorf("%w: amount %q is finer than the currency", ErrUnsupported, text)
		}
		frac = frac[:scale]
	}
	frac += strings.Repeat("0", scale-len(frac))
	var n int64
	for _, r := range whole + frac {
		if r < '0' || r > '9' || n > (1<<62)/10 {
			return 0, fmt.Errorf("%w: amount %q", ErrUnsupported, text)
		}
		n = n*10 + int64(r-'0')
	}
	if negative {
		n = -n
	}
	return n, nil
}
