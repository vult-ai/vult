package reporting

import "testing"

func TestCashMovementReportRetainsEveryContributionAndUnknown(t *testing.T) {
	in := dashboardFixture()
	r, err := Build(in, Request{BookID: "example", Report: CashMovement, From: "2026-01-01", To: "2026-01-31"})
	if err != nil || r.Status != "partial" || len(r.Rows) != 5 {
		t.Fatal("recorded cash movement report unavailable", err)
	}
	want := map[string]string{"cash:in": "1250", "cash:out": "100", "cash:net": "1150", "cash:effects": "30", "cash:closing": "11180"}
	for _, row := range r.Rows {
		parts, err := Contributions(in, r, row.ID, "current")
		if err != nil || row.Values["current"] != want[row.ID] || parts.Total != want[row.ID] {
			t.Fatal("cash summary cannot explain its exact postings", row.ID, err)
		}
	}
	for i := range in.Accounts {
		in.Accounts[i].Role = ""
	}
	r, err = Build(in, Request{BookID: "example", Report: CashMovement, From: "2026-01-01", To: "2026-01-31"})
	if err != nil || r.Status != "unsupported" || len(r.Rows) != 0 {
		t.Fatal("unclassified cash scope became an empty zero report", err)
	}
}
