package reporting

import (
	"math/big"
	"sort"
)

func mapAdd(target map[string]string, source map[string]string) {
	for key, value := range source {
		if target[key] == "" {
			target[key] = "0"
		}
		target[key] = new(big.Int).Add(integer(target[key]), integer(value)).String()
	}
}
func mapNonzero(values map[string]string) bool {
	for _, value := range values {
		if integer(value).Sign() != 0 {
			return true
		}
	}
	return false
}

func accountOrder(accounts []Account) {
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Code != accounts[j].Code {
			return accounts[i].Code < accounts[j].Code
		}
		return accounts[i].ID < accounts[j].ID
	})
}

func section(in Input, id, label string, categories []string, current, prior map[string]*big.Int, period Period, comparison *Period, useNatural bool) Row {
	eligible := map[string]Account{}
	byParent := map[string][]Account{}
	for _, a := range in.Accounts {
		for _, category := range categories {
			if class(a.Kind) == category {
				eligible[a.ID] = a
				break
			}
		}
	}
	for _, a := range eligible {
		parent := a.ParentID
		if _, ok := eligible[parent]; !ok {
			parent = ""
		}
		byParent[parent] = append(byParent[parent], a)
	}
	for parent := range byParent {
		accountOrder(byParent[parent])
	}
	ownActivity := map[string]bool{}
	for _, p := range in.Postings {
		if (inPeriod(p, period) || (comparison != nil && inPeriod(p, *comparison))) && ((len(categories) != 1 || (categories[0] != "income" && categories[0] != "expense")) || period.From == "" || earned(p)) {
			ownActivity[p.AccountID] = true
		}
	}
	exclude := period.From != "" && len(categories) == 1 && (categories[0] == "income" || categories[0] == "expense")
	var build func(Account) Row
	build = func(a Account) Row {
		cur := new(big.Int).Set(current[a.ID])
		prev := zero()
		if prior != nil {
			prev.Set(prior[a.ID])
		}
		if useNatural {
			cur = natural(a, cur)
			prev = natural(a, prev)
		}
		own := valueColumns(cur, prev, prior != nil)
		row := simpleRow("account:"+a.ID, a.Name, "account", map[string]string{}, &Drilldown{AccountIDs: []string{}, From: period.From, To: period.To, ExcludeClosing: exclude, ExcludeOpening: exclude})
		row.AccountID = a.ID
		row.AccountCode = a.Code
		if !a.Virtual {
			row.GLCode = a.GLCode
		}
		row.OwnValues = own
		mapAdd(row.Values, own)
		if !a.Virtual {
			row.Drilldown.AccountIDs = append(row.Drilldown.AccountIDs, a.ID)
		}
		for _, child := range byParent[a.ID] {
			built := build(child)
			mapAdd(row.Values, built.Values)
			row.Drilldown.AccountIDs = append(row.Drilldown.AccountIDs, built.Drilldown.AccountIDs...)
			if mapNonzero(built.Values) || len(built.Children) > 0 || ownActivity[child.ID] {
				row.Children = append(row.Children, built)
			}
		}
		sort.Strings(row.Drilldown.AccountIDs)
		if len(row.Children) > 0 && !a.Virtual && (mapNonzero(own) || ownActivity[a.ID]) {
			direct := simpleRow("direct:"+a.ID, "Direct postings", "direct", own, &Drilldown{AccountIDs: []string{a.ID}, From: period.From, To: period.To, ExcludeClosing: exclude, ExcludeOpening: exclude})
			direct.AccountID = a.ID
			direct.AccountCode = a.Code
			direct.GLCode = a.GLCode
			direct.OwnValues = own
			row.Children = append([]Row{direct}, row.Children...)
		}
		return row
	}
	result := simpleRow("section:"+id, label, "section", valueColumns(zero(), zero(), prior != nil), &Drilldown{AccountIDs: []string{}, From: period.From, To: period.To, ExcludeClosing: exclude, ExcludeOpening: exclude})
	for _, root := range byParent[""] {
		row := build(root)
		mapAdd(result.Values, row.Values)
		result.Drilldown.AccountIDs = append(result.Drilldown.AccountIDs, row.Drilldown.AccountIDs...)
		if mapNonzero(row.Values) || len(row.Children) > 0 || ownActivity[root.ID] {
			result.Children = append(result.Children, row)
		}
	}
	sort.Strings(result.Drilldown.AccountIDs)
	return result
}

func trialValues(current, prior *big.Int, compare bool) map[string]string {
	out := map[string]string{"debit": "0", "credit": "0"}
	if current.Sign() > 0 {
		out["debit"] = current.String()
	} else if current.Sign() < 0 {
		out["credit"] = new(big.Int).Neg(current).String()
	}
	if compare {
		out["prior_debit"] = "0"
		out["prior_credit"] = "0"
		if prior.Sign() > 0 {
			out["prior_debit"] = prior.String()
		} else if prior.Sign() < 0 {
			out["prior_credit"] = new(big.Int).Neg(prior).String()
		}
		out["variance"] = new(big.Int).Sub(current, prior).String()
	}
	return out
}
func trialTree(in Input, current, prior map[string]*big.Int, period Period) Row {
	byParent := map[string][]Account{}
	for _, a := range in.Accounts {
		byParent[a.ParentID] = append(byParent[a.ParentID], a)
	}
	for parent := range byParent {
		accountOrder(byParent[parent])
	}
	var build func(Account) Row
	build = func(a Account) Row {
		prev := zero()
		if prior != nil {
			prev = prior[a.ID]
		}
		own := trialValues(current[a.ID], prev, prior != nil)
		row := simpleRow("account:"+a.ID, a.Name, "account", map[string]string{}, &Drilldown{AccountIDs: []string{}, To: period.To})
		row.AccountID = a.ID
		row.AccountCode = a.Code
		if !a.Virtual {
			row.GLCode = a.GLCode
		}
		row.OwnValues = own
		mapAdd(row.Values, own)
		if !a.Virtual {
			row.Drilldown.AccountIDs = append(row.Drilldown.AccountIDs, a.ID)
		}
		for _, child := range byParent[a.ID] {
			r := build(child)
			row.Children = append(row.Children, r)
			mapAdd(row.Values, r.Values)
			row.Drilldown.AccountIDs = append(row.Drilldown.AccountIDs, r.Drilldown.AccountIDs...)
		}
		if len(row.Children) > 0 && !a.Virtual && mapNonzero(own) {
			direct := simpleRow("direct:"+a.ID, "Direct account balance", "direct", own, &Drilldown{AccountIDs: []string{a.ID}, To: period.To})
			direct.AccountID = a.ID
			direct.AccountCode = a.Code
			direct.GLCode = a.GLCode
			direct.OwnValues = own
			row.Children = append([]Row{direct}, row.Children...)
		}
		sort.Strings(row.Drilldown.AccountIDs)
		return row
	}
	result := simpleRow("section:trial_balance", "Trial Balance", "section", trialValues(zero(), zero(), prior != nil), &Drilldown{AccountIDs: []string{}, To: period.To})
	for _, a := range byParent[""] {
		r := build(a)
		result.Children = append(result.Children, r)
		mapAdd(result.Values, r.Values)
		result.Drilldown.AccountIDs = append(result.Drilldown.AccountIDs, r.Drilldown.AccountIDs...)
	}
	sort.Strings(result.Drilldown.AccountIDs)
	return result
}
