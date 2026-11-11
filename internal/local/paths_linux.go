package local

const (
	SocketPath         = "/var/lib/vult/socket/service.sock"
	BinaryPath         = "/opt/vult/vult"
	DataPath           = "/var/lib/vult/data"
	ServiceUser        = "_vultledger"
	BrowserUser        = "_vultbrowser"
	BrowserPath        = "/var/lib/vult/browser"
	PostgresSocketPath = "/var/lib/vult/postgres"
	PostgresPath       = "/opt/vult/postgres"
	// RecoveryPath holds recovery points: root-only, never in a user's home.
	RecoveryPath = "/var/lib/vult/recovery"
)
