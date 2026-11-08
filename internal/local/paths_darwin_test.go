package local

import "testing"

func TestMacOSInstallationPathsStayStable(t *testing.T) {
	paths := [][2]string{
		{SocketPath, "/Library/Application Support/ai.vult.ledger/socket/service.sock"},
		{BinaryPath, "/Library/PrivilegedHelperTools/ai.vult.ledger/vult"},
		{DataPath, "/Library/Application Support/ai.vult.ledger/data"},
		{BrowserPath, "/Library/Application Support/ai.vult.ledger/browser"},
		{PostgresSocketPath, "/Library/Application Support/ai.vult.ledger/postgres"},
		{PostgresPath, "/Library/PrivilegedHelperTools/ai.vult.ledger/postgres"},
	}
	for _, path := range paths {
		if path[0] != path[1] {
			t.Fatal("existing installation path changed", path[0])
		}
	}
	if ServiceUser != "_vultledger" || BrowserUser != "_vultbrowser" {
		t.Fatal("existing service identities changed")
	}
}
