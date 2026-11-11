package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/ledger"
)

// Maintenance commands. Each runs as the service account while the service
// is stopped, with PostgreSQL reachable only by that account.
const (
	MaintenanceMigrate     = "migrate"     // initialize, or move the schema forward
	MaintenanceValidate    = "validate"    // check the schema is current
	MaintenanceFingerprint = "fingerprint" // print the fact fingerprint
	// MaintenanceArchive writes the whole installation's portable archive,
	// encrypted with the installation recovery key, to standard output.
	MaintenanceArchive = "recovery-archive"
	// MaintenanceRestore restores a plaintext portable archive read from
	// standard input into this installation, which must be empty.
	MaintenanceRestore = "restore"
)

// maintenanceTimeout bounds one maintenance command, including a migration
// of a large database.
const maintenanceTimeout = 30 * time.Minute

// MaintenanceReport is what a maintenance command prints, as JSON.
type MaintenanceReport struct {
	Command     string                  `json:"command"`
	Applied     int                     `json:"applied"` // migrations recorded now
	Known       int                     `json:"known"`   // migrations this release has
	Schema      string                  `json:"schema"`  // the newest recorded migration's number
	Migration   *ledger.MigrationResult `json:"migration,omitempty"`
	Fingerprint ledger.FactFingerprint  `json:"fingerprint"`
}

// ErrServiceRunning means a maintenance command found the service running.
var ErrServiceRunning = errors.New("stop the Vult service before running its maintenance step")

// Maintenance runs one maintenance command and writes its report (or, for
// MaintenanceArchive, the archive) to out. Only MaintenanceRestore reads in.
func Maintenance(ctx context.Context, command string, in io.Reader, out io.Writer) error {
	switch command {
	case MaintenanceMigrate, MaintenanceValidate, MaintenanceFingerprint, MaintenanceArchive, MaintenanceRestore:
	default:
		return ledger.ErrInvalid
	}
	if !nativeSupported() {
		return errors.New("maintenance requires a supported installation")
	}
	if err := nativeStartupReady(); err != nil {
		return err
	}
	uid, _, e := serviceIdentity()
	if e != nil || uint32(os.Geteuid()) != uid {
		return errors.New("maintenance must run under the service account")
	}
	if e = installationCode(); e != nil {
		return e
	}
	conf, e := loadConfiguration(uid)
	if e != nil {
		return e
	}
	if conf.State != "ready" && command != MaintenanceMigrate {
		return errors.New("Vult has not finished installing")
	}
	if e = os.Chdir(local.DataPath); e != nil {
		return e
	}
	// The service holds this lock while it runs; maintenance never runs
	// beside it.
	lock, e := os.OpenFile(filepath.Join(local.DataPath, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return ErrServiceRunning
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	work, cancel := context.WithTimeout(ctx, maintenanceTimeout)
	defer cancel()
	database := filepath.Join(local.DataPath, "database")
	// A PostgreSQL left running by a stopped service may be listening for
	// other accounts; restart it privately.
	if runPostgres(work, "pg_ctl", "-D", database, "status") == nil {
		if e = runPostgres(work, "pg_ctl", "-D", database, "-m", "fast", "-w", "-t", "60", "stop"); e != nil {
			return e
		}
	}
	if e = prepareDatabase(work, conf, true); e != nil {
		return e
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 75*time.Second)
		defer cancel()
		_ = runPostgres(stop, "pg_ctl", "-D", database, "-m", "fast", "-w", "-t", "60", "stop")
	}()
	admin, e := openDatabase(work, conf)
	if e != nil {
		return e
	}
	defer admin.Close()

	if command == MaintenanceArchive || command == MaintenanceRestore {
		if e = ledger.RequireCurrent(work, admin); e != nil {
			return e
		}
		storage, e := prepareDocumentStorage(work, ledger.Admin{DB: admin}, local.DataPath)
		if e != nil {
			return e
		}
		defer storage.Close()
		if command == MaintenanceArchive {
			return writeRecoveryArchive(work, admin, storage, conf.RecoveryKey, out)
		}
		if e = restoreRecoveryArchive(work, admin, storage, in); e != nil {
			return e
		}
	}
	report := MaintenanceReport{Command: command}
	if command == MaintenanceMigrate {
		result, e := migrateSchema(work, admin, "vult_runtime", conf.State)
		if e != nil {
			var refusal *ledger.UpgradeRefusal
			if errors.As(e, &refusal) || errors.Is(e, ledger.ErrFactsChanged) {
				return e
			}
			return fmt.Errorf("the database update did not complete and nothing was changed: %w", e)
		}
		report.Migration = &result
		if conf.State == "new" {
			if e = markConfigurationReady(filepath.Join(local.DataPath, "configuration.json"), conf); e != nil {
				return e
			}
		}
	}
	state, e := ledger.Schema(work, admin)
	if e != nil {
		return e
	}
	report.Applied, report.Known, report.Schema = state.Applied, state.Known, state.Last
	if command != MaintenanceFingerprint && !state.Current() {
		return ledger.ErrNeedsMaintenance
	}
	if report.Fingerprint, e = ledger.Fingerprint(work, admin); e != nil {
		return e
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// recoveryWorkFactor is the scrypt cost for archives encrypted with the
// recovery key, the same as owner backups.
const recoveryWorkFactor = 18

func writeRecoveryArchive(ctx context.Context, admin *pgxpool.Pool, storage *documentStorage, key string, out io.Writer) error {
	if len(key) < 32 {
		return ledger.ErrInvalid
	}
	recipient, err := age.NewScryptRecipient(key)
	if err != nil {
		return err
	}
	recipient.SetWorkFactor(recoveryWorkFactor)
	cipher, err := age.Encrypt(out, recipient)
	if err != nil {
		return err
	}
	if err = (ledger.Admin{DB: admin}).WriteArchive(ctx, storage.vault, cipher, ""); err != nil {
		return err
	}
	return cipher.Close()
}

func restoreRecoveryArchive(ctx context.Context, admin *pgxpool.Pool, storage *documentStorage, in io.Reader) error {
	if in == nil {
		return ledger.ErrInvalid
	}
	selected := ""
	provision := func(ctx context.Context, tenant string) error {
		if selected != "" && selected != tenant {
			return ledger.ErrInvalid
		}
		selected = tenant
		return storage.keys.Provision(ctx, tenant)
	}
	return (ledger.Admin{DB: admin}).RestoreArchive(ctx, storage.vault, in, provision)
}
