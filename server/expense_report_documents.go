package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/internal/pdfwrite"
	"github.com/vult-ai/vult/ledger"
)

// Documents of a report: an approved revision as filed, and dated
// statements of what was paid. Both are rendered here, stored as
// ordinary documents in the business's Book with their hashes recorded,
// and never made again: a stored one stays as it was.

// money writes minor units in a Book's currency.
func money(minor, currency string) string {
	n, err := strconv.ParseInt(minor, 10, 64)
	if err != nil {
		return minor
	}
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	whole, cents := strconv.FormatInt(n/100, 10), n%100
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	symbol := currency + " "
	if currency == "USD" {
		symbol = "$"
	}
	return sign + symbol + whole + "." + strconv.FormatInt(cents/10, 10) + strconv.FormatInt(cents%10, 10)
}

var capacityNames = map[string]string{"employee": "employee", "shareholder_employee": "shareholder-employee", "partner": "partner", "sole_owner": "sole owner", "not_established": "relationship not established"}

func reportPDF(d ledger.ReportDetail, revision ledger.RevisionView) []byte {
	doc := pdfwrite.New("Expense report " + d.Reference)
	doc.Text(18, true, "Expense report "+d.Reference)
	doc.Text(10, false, d.BusinessName+" pays back "+d.Person.Name+" ("+capacityNames[d.Person.Capacity]+")")
	doc.Text(10, false, "Revision "+strconv.Itoa(revision.Revision)+" · "+d.Currency)
	if revision.SubmittedAt != nil {
		doc.Text(10, false, "Submitted "+revision.SubmittedAt.UTC().Format("January 2, 2006")+" with the attestation: These are business costs I paid, and nobody has paid me back for them.")
	}
	if revision.ApprovedAt != nil {
		approved := "Approved " + revision.ApprovedAt.UTC().Format("January 2, 2006")
		if revision.SelfApproved {
			approved += ". Self-approved: the owner approved their own report."
		}
		doc.Text(10, false, approved)
	}
	if revision.Reason != "" {
		doc.Text(10, false, "Why this revision: "+revision.Reason)
	}
	doc.Gap(10)
	doc.Text(12, true, "Costs")
	for _, x := range d.Lines {
		amount := x.AmountMinor
		if x.ApprovedMinor != nil {
			amount = *x.ApprovedMinor
		}
		account := x.AccountName
		if account == "" {
			account = "account to be chosen"
		}
		doc.Text(10, true, x.Date+"  "+x.Vendor+"  "+money(amount, d.Currency))
		doc.Text(9, false, "For: "+x.Purpose+" · Account: "+account)
		receipt := "No receipt"
		if len(x.Evidence) > 0 {
			var refs []string
			for _, e := range x.Evidence {
				refs = append(refs, "document "+e.DocumentID+" (sha256 "+e.SHA256[:12]+"…)")
			}
			receipt = "Receipt: " + strings.Join(refs, ", ")
		} else if x.MissingExplanation != "" {
			receipt = "No receipt: " + x.MissingExplanation
		}
		doc.Text(9, false, receipt)
		if x.Provenance != "" {
			doc.Text(9, false, "Owed before: "+x.Provenance)
		}
	}
	doc.Gap(10)
	doc.Text(11, true, "Total approved "+money(d.ApprovedMinor, d.Currency))
	doc.Text(10, false, "Paid "+money(d.PaidMinor, d.Currency)+" · Left to pay "+money(d.RemainingMinor, d.Currency))
	doc.Text(10, false, "Bank memo: "+d.Memo)
	doc.Gap(8)
	doc.Text(8, false, "Prepared by Vult from the business's records. It records what was submitted and approved; it is not tax advice.")
	return doc.Bytes()
}

func statementPDF(d ledger.ReportDetail) ([]byte, string) {
	doc := pdfwrite.New("Payment statement " + d.Reference)
	doc.Text(18, true, "Payment statement for "+d.Reference)
	doc.Text(10, false, d.BusinessName+" pays back "+d.Person.Name)
	doc.Gap(8)
	doc.Text(12, true, "Payments")
	var listed []string
	for _, p := range d.Payments {
		line := p.Date + "  " + money(p.AmountMinor, d.Currency)
		if p.RemovedAt != nil {
			line += "  (unmatched " + p.RemovedAt.UTC().Format("January 2, 2006") + ")"
		}
		if p.Unapproved {
			line += "  (paid before approval)"
		}
		doc.Text(10, false, line)
		listed = append(listed, p.AllocationID+":"+p.AmountMinor+":"+strconv.FormatBool(p.RemovedAt == nil))
	}
	if len(d.Payments) == 0 {
		doc.Text(10, false, "No payment yet.")
	}
	doc.Gap(8)
	doc.Text(11, true, "Approved "+money(d.ApprovedMinor, d.Currency)+" · Paid "+money(d.PaidMinor, d.Currency)+" · Left to pay "+money(d.RemainingMinor, d.Currency))
	for _, r := range d.Recoverables {
		state := "to be resolved"
		if r.Resolution != nil {
			state = *r.Resolution
		}
		doc.Text(10, false, "Owed back to "+d.BusinessName+": "+money(r.AmountMinor, d.Currency)+" ("+state+")")
	}
	sum := sha256.Sum256([]byte(strings.Join(listed, "\n")))
	return doc.Bytes(), hex.EncodeToString(sum[:])
}

// storeRevisionPDF files an approved revision's document once.
func (a *webAPI) storeRevisionPDF(ctx context.Context, session, tenant, book, reference string, revision int) error {
	var d ledger.ReportDetail
	err := a.sessions.WithOwner(ctx, session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		d, err = o.ExpenseReport(ctx, book, reference)
		return err
	})
	if err != nil {
		return err
	}
	var target *ledger.RevisionView
	for i := range d.Revisions {
		if d.Revisions[i].Revision == revision {
			target = &d.Revisions[i]
		}
	}
	if target == nil || target.PDFDocumentID != "" {
		return nil
	}
	id, _, err := a.storeDocument(ctx, session, tenant, book, "report-"+d.ID+"-"+strconv.Itoa(revision), d.Reference+" revision "+strconv.Itoa(revision)+".pdf", reportPDF(d, *target))
	if err != nil {
		return err
	}
	return a.sessions.OwnerAct(ctx, session, tenant, func(o ledger.TenantAdmin, who string) error {
		return o.SetRevisionDocument(ctx, book, who, reference, revision, id)
	})
}

// issueStatement files a dated statement of what was paid on a report.
func (a *webAPI) issueStatement(ctx context.Context, session, tenant, book, reference string) (ledger.StatementView, error) {
	var out ledger.StatementView
	var d ledger.ReportDetail
	err := a.sessions.WithOwner(ctx, session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		d, err = o.ExpenseReport(ctx, book, reference)
		return err
	})
	if err != nil {
		return out, err
	}
	data, allocations := statementPDF(d)
	id, _, err := a.storeDocument(ctx, session, tenant, book, "statement-"+d.ID+"-"+allocations[:16], d.Reference+" payment statement.pdf", data)
	if err != nil {
		return out, err
	}
	err = a.sessions.OwnerAct(ctx, session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		out, err = o.RecordSettlementStatement(ctx, book, who, reference, id, allocations)
		return err
	})
	return out, err
}
