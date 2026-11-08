//go:build !linux

package local

const (
	SocketPath         = "/Library/Application Support/ai.vult.ledger/socket/service.sock"
	BinaryPath         = "/Library/PrivilegedHelperTools/ai.vult.ledger/vult"
	DataPath           = "/Library/Application Support/ai.vult.ledger/data"
	ServiceUser        = "_vultledger"
	BrowserUser        = "_vultbrowser"
	BrowserPath        = "/Library/Application Support/ai.vult.ledger/browser"
	PostgresSocketPath = "/Library/Application Support/ai.vult.ledger/postgres"
	PostgresPath       = "/Library/PrivilegedHelperTools/ai.vult.ledger/postgres"
)
