package setaside

import "testing"

func TestTheReleasesOwnAddressesStayAndTheRestAreSetAside(t *testing.T) {
	for _, p := range []string{"/api/v1/bank", "/api/v1/bank/activity", "/api/v1/agent-chat", "/api/v1/projects/report.csv", "/api/v1/books/deletion/confirm", "/api/v1/book-links/opening"} {
		if !Path(p) {
			t.Error("not set aside:", p)
		}
	}
	for _, p := range []string{"/api/v1/backups", "/api/v1/backups/restore", "/api/v1/books/archive", "/api/v1/books", "/api/v1/bankruptcy", "/api/v1/agent-owner/plans", "/api/v1/statements"} {
		if Path(p) {
			t.Error("set aside but this release's:", p)
		}
	}
}
