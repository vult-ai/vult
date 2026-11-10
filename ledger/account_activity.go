package ledger

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type AccountHistoryRead struct {
	Weekly   []int64 `json:"weekly"`
	Total    int64   `json:"total"`
	LastDate string  `json:"last_date"`
}
type AccountGroupRead struct {
	Codes    []string `json:"account_codes"`
	Weekly   []int64  `json:"weekly"`
	Count    int64    `json:"count"`
	LastDate string   `json:"last_date"`
}
type AccountActivityRead struct {
	RecordedState
	SchemaVersion    int                            `json:"schema_version"`
	Version          int                            `json:"version"`
	BookID           string                         `json:"book_id"`
	Currency         string                         `json:"currency"`
	Scale            int                            `json:"scale"`
	AsOf             string                         `json:"as_of"`
	From             string                         `json:"from"`
	Accounts         map[string]*AccountHistoryRead `json:"accounts"`
	EmptyWeekly      []int64                        `json:"empty_weekly"`
	Groups           []*AccountGroupRead            `json:"groups"`
	NativeBalances   map[string]string              `json:"native_balances"`
	NativeCurrencies map[string]string              `json:"native_currencies"`
	NativeScales     map[string]int                 `json:"native_scales"`
	NativeStatus     map[string]string              `json:"native_status"`
	ActivityBasis    string                         `json:"activity_basis"`
	HierarchyBasis   string                         `json:"hierarchy_basis"`
	SourceCoverage   string                         `json:"source_coverage"`
	Digest           string                         `json:"content_sha256"`
}

func activityStart(asOf string) (time.Time, error) {
	if !ValidDate(asOf) {
		return time.Time{}, ErrInvalid
	}
	end, _ := time.Parse("2006-01-02", asOf)
	start := end.AddDate(0, 0, -363)
	if !ValidDate(start.Format("2006-01-02")) {
		return time.Time{}, ErrInvalid
	}
	return start, nil
}

func (a TenantAdmin) AccountActivity(ctx context.Context, book, asOf string, ids []string) (AccountActivityRead, error) {
	selected, err := accountIDs(ids)
	if err != nil {
		return AccountActivityRead{}, err
	}
	if _, err := activityStart(asOf); err != nil {
		return AccountActivityRead{}, err
	}
	var out AccountActivityRead
	err = a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) error {
		in, err := readAccountHistory(ctx, tx, scope, header, asOf, selected, false)
		if err != nil {
			return err
		}
		out, err = buildAccountActivity(in)
		return err
	})
	if err != nil {
		return AccountActivityRead{}, err
	}
	return out, nil
}

// A group is a set of stable account IDs, never a path decoded from a name.
// An entry contributes once to each set even if several selected lines match.
func buildAccountActivity(in accountHistory) (AccountActivityRead, error) {
	start, err := activityStart(in.AsOf)
	if err != nil {
		return AccountActivityRead{}, err
	}
	out := AccountActivityRead{SchemaVersion: 1, Version: 1, BookID: in.Book.ID, Currency: in.Book.Currency, Scale: in.Book.Scale, AsOf: in.AsOf, From: start.Format("2006-01-02"), Accounts: map[string]*AccountHistoryRead{}, EmptyWeekly: make([]int64, 52), Groups: []*AccountGroupRead{}, NativeBalances: map[string]string{}, NativeCurrencies: map[string]string{}, NativeScales: map[string]int{}, NativeStatus: map[string]string{}, ActivityBasis: "recorded_entries", HierarchyBasis: "flat_recorded_accounts", SourceCoverage: "unknown"}
	sets := map[string][]string{}
	classes := map[string][]string{}
	all := []string{}
	for _, account := range in.Accounts {
		out.Accounts[account.ID] = &AccountHistoryRead{Weekly: make([]int64, 52)}
		sets[account.ID] = []string{account.ID}
		classes[account.Kind] = append(classes[account.Kind], account.ID)
		all = append(all, account.ID)
		out.NativeStatus[account.ID] = account.NativeStatus
		if account.NativeStatus == "complete" && account.NativeMinor != nil && account.Scale != nil {
			out.NativeBalances[account.ID] = *account.NativeMinor
			out.NativeCurrencies[account.ID] = account.Currency
			out.NativeScales[account.ID] = *account.Scale
		}
	}
	for _, ids := range classes {
		sort.Strings(ids)
		sets[strings.Join(ids, "\x00")] = ids
	}
	// A recorded parent's group is the parent and every account beneath it.
	children := map[string][]string{}
	for _, account := range in.Accounts {
		if account.ParentID != "" {
			children[account.ParentID] = append(children[account.ParentID], account.ID)
		}
	}
	for _, account := range in.Accounts {
		if len(children[account.ID]) == 0 {
			continue
		}
		ids, seen := []string{}, map[string]bool{}
		for stack := []string{account.ID}; len(stack) > 0; {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
			stack = append(stack, children[id]...)
		}
		sort.Strings(ids)
		sets[strings.Join(ids, "\x00")] = ids
	}
	if len(children) > 0 {
		out.HierarchyBasis = "recorded_account_parents"
	}
	if len(all) > 0 {
		sort.Strings(all)
		sets[strings.Join(all, "\x00")] = all
	}
	keys := make([]string, 0, len(sets))
	for key := range sets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	membership := map[string][]int{}
	seen := make([]map[string]bool, 0, len(keys))
	for _, key := range keys {
		ids := sets[key]
		index := len(out.Groups)
		out.Groups = append(out.Groups, &AccountGroupRead{Codes: ids, Weekly: make([]int64, 52)})
		seen = append(seen, map[string]bool{})
		for _, id := range ids {
			membership[id] = append(membership[id], index)
		}
	}
	accountSeen := map[string]map[string]bool{}
	for _, line := range in.Lines {
		history := out.Accounts[line.AccountID]
		if history == nil {
			return AccountActivityRead{}, ErrDenied
		}
		day, _ := time.Parse("2006-01-02", line.Date)
		bucket := -1
		if !day.Before(start) {
			bucket = int(day.Sub(start)/(24*time.Hour)) / 7
		}
		if bucket > 51 {
			return AccountActivityRead{}, ErrDenied
		}
		if accountSeen[line.AccountID] == nil {
			accountSeen[line.AccountID] = map[string]bool{}
		}
		if !accountSeen[line.AccountID][line.EntryID] {
			accountSeen[line.AccountID][line.EntryID] = true
			history.Total++
			if line.Date > history.LastDate {
				history.LastDate = line.Date
			}
			if bucket >= 0 {
				history.Weekly[bucket]++
			}
		}
		for _, index := range membership[line.AccountID] {
			if seen[index][line.EntryID] {
				continue
			}
			seen[index][line.EntryID] = true
			group := out.Groups[index]
			if line.Date > group.LastDate {
				group.LastDate = line.Date
			}
			if bucket >= 0 {
				group.Weekly[bucket]++
				group.Count++
			}
		}
	}
	out.Digest = archiveHash(raw(out))
	out.RecordedState = in.State
	return out, nil
}
