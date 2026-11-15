package ledger_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// frozenMigrations are migrations a database may already have applied:
// those in a released or candidate installer, and those applied to a shared
// development database. Each is recorded by its checksum, and a database
// refuses to start when an applied migration differs, so they never change.
// A change goes into a new, higher-numbered migration.
var frozenMigrations = map[string]string{
	"0001_ledger.sql":                                "1f2a80022713d3b913555391319685f3042916b437260912a7e1c8743f53e451",
	"0002_server_access.sql":                         "733594d4b3559f30f8f97ed2ca3595b8f8cf6d2adf4b2cbd0213a32da5f4814f",
	"0003_documents.sql":                             "1427a42e8c4d90d0fa5f3de6d8ce56c9467e243a336f86758e5d98dd8704fbe6",
	"0004_recorded_book.sql":                         "e40086acd4c7bd996751bf5c5b7dadc90b499ea7e742df3080884782813d4ab2",
	"0005_proposing_agents.sql":                      "f867faf816b0ff040c02eeb241010f223dfb1aa6fb033645aae1a8f7470f655e",
	"0006_owner_recovery_codes.sql":                  "4e17ccd1ac8bf931d59b434b15711e7fecdddd7982b086cc6377b52e80454272",
	"0007_accounting_foundations.sql":                "5a1510de7019504794eb3e7b7bc168cbd241c8846490ecdd473eb344140dfb31",
	"0008_bank_activity_agents_and_linked_books.sql": "dc2ab3ebf061948b36d6045f7260adb2e9a17d88c0ef41f4835899770536193a",
}

// bridgeMigrations are 0.8's steps as its release candidates applied them,
// one by one. A Vult that took some of them takes the rest, as they were,
// before it records the squashed 0.8 migration, so they never change either.
var bridgeMigrations = map[string]string{
	"0008_bank_activity.sql":                       "912fe87033f132d0b0fdbeaacfcbe6db351f0f31762e9f923ad56dfd26999d0a",
	"0009_recording_rules.sql":                     "82309bf917fd9fe344f4fdbbeb82154aa48a977866a83dc91615a6bce7dac85b",
	"0010_agent_settings.sql":                      "22b7078249f8b6344acb47f035df033b38101c0b64997dcf77d394c33405fc34",
	"0011_agent_autonomy.sql":                      "b96e60591bbae31b760778c9218346aa322bdd425b5eadeb809053b99722b280",
	"0012_owner_paid.sql":                          "895ec8909355b024f06a1b1c51ed32b82fc5be1547561335abc54a331090296b",
	"0013_linked_books.sql":                        "e48b1689ac29af635e3502dacbb45e39061bb30d840d169551aa1305d7af65ac",
	"0014_vult_keys_and_document_groups.sql":       "258daea3bcb7d2979d1ca906907893322bd8e508f5c7f0fbdde8c33177933d4d",
	"0015_linked_books_settling.sql":               "f5be55fbceabaac9bb5805b700bf8699a982571c8085d5f9175696950c7a08f6",
	"0016_linked_books_followups.sql":              "99dfab337c140f4267f3b31a93d63046c2d2cfdc0bdd12c838912bd5f46ce88a",
	"0017_book_archive_and_erasure.sql":            "87421f671f1f30ff7e4e641147557122e9fc3891afd5f8209f9bf580b102c9e6",
	"0018_mercury_connections.sql":                 "ea9e650c745dba1fefefe30e91071bc534131f46f5a66d602aad19955f295826",
	"0019_bank_account_takeovers.sql":              "4d92bd1672fa461cf406b04f1dc93c31a2a509072353b0ee052e005a9c3305d7",
	"0020_document_checks.sql":                     "cd525f0cc0393873efef445f68721d1eef4d0514ddae0a647f203d6ef07607dd",
	"0021_vult_agent_key.sql":                      "0082dbed388c19e17d9cbfa63ae3b91607a69103603c90634f71373b8ecdbf3e",
	"0022_partial_repayments.sql":                  "99d904204ad5fb216d504e4189d71de01dc064bd88e581843dbd65b747033bd2",
	"0023_clear_refusals.sql":                      "9fa179a4c664ed3d24a39ae64c1cc2b8fd237a3ad23d2578221047eca9b1427b",
	"0024_book_tax_classification.sql":             "a0b73166c12f478ccb9278a312ffdb2559432ff995f4a863de442792adaeb406",
	"0030_linked_balance_totals.sql":               "589c5bf5d8200f4d2690575da5fe0599ee70f0dd77d041e88cf32d8351f2ad53",
	"0031_projects.sql":                            "8c9162382ad194f3aba1888b5b994beacf6eee3eab7b36c3ae79f99ddf9c3150",
	"0032_carried_costs.sql":                       "abd6cf812073411b4434957bbc35d762313d292dc2b8c25412a61d3265662168",
	"0033_expense_reports.sql":                     "a3693902a1c47290db24acda6f8d3271b09f8eea8f1d572a7e7a7f0340ba6738",
	"0034_bank_lookups_by_index.sql":               "3d72f9dc7696109efaf93cb6cd3295506df015d7b1f129a2afd8b38fdea3d421",
	"0035_counterparty_keys_in_every_alphabet.sql": "b6e8a438e2b4b8126cc251102b7b143241b43171faa1004062ea1fe6ed81b7c5",
}

// unreleasedMigrations are known but not yet released: they may still
// change, and join frozenMigrations with their checksum when they ship.
var unreleasedMigrations = map[string]bool{}

func TestFrozenMigrationsNeverChange(t *testing.T) {
	all := map[string]string{}
	for name, want := range frozenMigrations {
		all["migrations/"+name] = want
	}
	for name, want := range bridgeMigrations {
		all["migrations/bridge/"+name] = want
	}
	for name, want := range all {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Errorf("%s is gone: databases that applied it would refuse to start", name)
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s changed (%s): databases that applied it refuse to start; put the change in a new migration instead", name, got)
		}
	}
}

// The migrations folder holds 0.7.0's seven and 0.8 in one; the bridge
// holds 0.8's steps, nothing else.
func TestMigrationFoldersHoldOnlyTheKnownFiles(t *testing.T) {
	for dir, known := range map[string]map[string]string{"migrations": frozenMigrations, "migrations/bridge": bridgeMigrations} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if dir == "migrations" && unreleasedMigrations[e.Name()] {
				n++
				continue
			}
			if _, ok := known[e.Name()]; !ok {
				t.Errorf("%s/%s isn't a known migration; after 0.8 a change goes into a new, higher-numbered one next to 0008_bank_activity_agents_and_linked_books.sql", dir, e.Name())
			}
			n++
		}
		if dir == "migrations" {
			n -= len(unreleasedMigrations)
		}
		if n != len(known) {
			t.Errorf("%s holds %d migrations, %d known", dir, n, len(known))
		}
	}
}
