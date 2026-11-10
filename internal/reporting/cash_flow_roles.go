package reporting

import (
	"math/big"
	"sort"
)

// RoleCashFlowReference names the published rule that classifies cash lines
// from the account roles on the other side of their entry.
const RoleCashFlowReference = "account-roles-direct-v1"

// RoleCashFlowPolicy derives a direct-method cash-flow policy from recorded
// account classes and roles, never from names, memos, payees or codes.
//
// The cash scope is every non-virtual asset account with role cash. Each cash
// line is classified by the other side of its entry:
//   - lines between cash accounts are internal transfers and net to zero;
//   - an equity account or a loan account is financing;
//   - a fixed_asset or investment account is investing;
//   - anything else is operating.
//
// An opening-balance entry, one that posts to an account with role
// opening_balance and to no income or expense, is where the Book's cash
// starts: its cash lines are opening cash, never an inflow.
//
// When the other side spans several sections, the cash lines are split by the
// other side's line amounts. A split that cannot be made exactly, with every
// part carrying its cash line's sign, is left unallocated and reported as
// unclassified rather than forced into a section.
func RoleCashFlowPolicy(in Input) CashFlowPolicy {
	accounts, _, entries := extensionIndex(in)
	policy := CashFlowPolicy{CashScopeConfirmed: true, Reference: RoleCashFlowReference, NoncashReviewConfirmed: true, Derived: true, CashAccountIDs: []string{}, Allocations: []CashFlowAllocation{}, Noncash: []NoncashActivity{}}
	cash := map[string]bool{}
	for _, a := range in.Accounts {
		if !a.Virtual && a.Kind == "asset" && a.Role == "cash" {
			cash[a.ID] = true
			policy.CashAccountIDs = append(policy.CashAccountIDs, a.ID)
		}
	}
	sort.Strings(policy.CashAccountIDs)
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var cashLines []Posting
		sections := map[string]*big.Int{}
		openingBalance, earned := false, false
		for _, p := range entries[id] {
			switch a := accounts[p.AccountID]; {
			case a.Role == "opening_balance" && class(a.Kind) == "equity":
				openingBalance = true
			case class(a.Kind) == "income" || class(a.Kind) == "expense":
				earned = true
			}
		}
		for _, p := range entries[id] {
			if cash[p.AccountID] {
				if p.Amount != "0" {
					cashLines = append(cashLines, p)
				}
				continue
			}
			section := roleCashSection(accounts[p.AccountID])
			if sections[section] == nil {
				sections[section] = new(big.Int)
			}
			// The cash effect of the other side is its negated signed amount.
			sections[section].Sub(sections[section], extensionSigned(p))
		}
		if len(cashLines) == 0 {
			continue
		}
		if openingBalance && !earned {
			for _, p := range cashLines {
				policy.Allocations = append(policy.Allocations, CashFlowAllocation{PostingID: p.ID, Amount: extensionSigned(p).String(), Category: "opening_balance", TreatmentReference: RoleCashFlowReference})
			}
			continue
		}
		policy.Allocations = append(policy.Allocations, roleAllocations(id, cashLines, sections)...)
	}
	return policy
}

func roleCashSection(a Account) string {
	switch {
	case class(a.Kind) == "equity", a.Kind == "liability" && a.Role == "loan":
		return "financing"
	case a.Kind == "asset" && (a.Role == "fixed_asset" || a.Role == "investment"):
		return "investing"
	default:
		return "operating"
	}
}

func roleAllocations(entry string, cashLines []Posting, sections map[string]*big.Int) []CashFlowAllocation {
	out := []CashFlowAllocation{}
	nonzero := []string{}
	for _, name := range []string{"operating", "investing", "financing"} {
		if v := sections[name]; v != nil && v.Sign() != 0 {
			nonzero = append(nonzero, name)
		}
	}
	if len(nonzero) == 0 {
		// Only cash accounts (or zero-value other lines): a transfer between
		// cash accounts, excluded from the sections and netting to zero.
		for _, p := range cashLines {
			out = append(out, CashFlowAllocation{PostingID: p.ID, Amount: extensionSigned(p).String(), Category: "internal_transfer", TreatmentReference: RoleCashFlowReference, TransferGroup: entry})
		}
		return out
	}
	if len(nonzero) == 1 && len(cashLines) == 1 {
		p := cashLines[0]
		return append(out, CashFlowAllocation{PostingID: p.ID, Amount: extensionSigned(p).String(), Category: nonzero[0], TreatmentReference: RoleCashFlowReference})
	}
	// Several cash lines or several sections: every cash line and every
	// section amount must share one sign, so the split is exact and keeps
	// each part's direction. Otherwise the entry stays unclassified.
	sign := extensionSigned(cashLines[0]).Sign()
	for _, p := range cashLines {
		if extensionSigned(p).Sign() != sign {
			return out
		}
	}
	remaining := map[string]*big.Int{}
	for _, name := range nonzero {
		if sections[name].Sign() != sign {
			return out
		}
		remaining[name] = new(big.Int).Set(sections[name])
	}
	for _, p := range cashLines {
		left := extensionSigned(p)
		for _, name := range nonzero {
			if left.Sign() == 0 {
				break
			}
			take := new(big.Int).Set(remaining[name])
			if new(big.Int).Abs(take).Cmp(new(big.Int).Abs(left)) > 0 {
				take.Set(left)
			}
			if take.Sign() == 0 {
				continue
			}
			out = append(out, CashFlowAllocation{PostingID: p.ID, Amount: take.String(), Category: name, TreatmentReference: RoleCashFlowReference})
			remaining[name].Sub(remaining[name], take)
			left.Sub(left, take)
		}
	}
	return out
}

// HasCashRole reports whether any account carries the cash role, so a
// role-derived cash-flow statement has a scope.
func HasCashRole(in Input) bool {
	for _, a := range in.Accounts {
		if !a.Virtual && a.Kind == "asset" && a.Role == "cash" {
			return true
		}
	}
	return false
}
