package ledger

import "testing"

// A backup taken by an earlier 0.8 build, which recorded 0.8 as one file,
// is one this release knows, as the release candidate's it stands for is.
func TestBackupsFromAnEarlier08BuildAreKnown(t *testing.T) {
	names := migrationNames()
	candidate := map[string]string{}
	for _, name := range names {
		if name == squashedMigration {
			break
		}
		candidate[name] = migrationSum(name)
	}
	for _, step := range bridgeNames() {
		if step > earlierSquashThrough {
			break
		}
		candidate[step] = bridgeSum(step)
	}
	if !knownMigrations(names, candidate) {
		t.Fatal("a release candidate's backup isn't known")
	}
	over := map[string]string{earlierSquash: earlierSquashSum}
	for name, sum := range candidate {
		over[name] = sum
	}
	if !knownMigrations(names, over) {
		t.Fatal("a backup from an earlier 0.8 build over a release candidate isn't known")
	}
	fresh := map[string]string{earlierSquash: earlierSquashSum}
	for _, name := range names {
		if name == squashedMigration {
			break
		}
		fresh[name] = migrationSum(name)
	}
	if !knownMigrations(names, fresh) {
		t.Fatal("a backup from a new Vult on an earlier 0.8 build isn't known")
	}
	fresh[earlierSquash] = "0000000000000000000000000000000000000000000000000000000000000000"
	if knownMigrations(names, fresh) {
		t.Fatal("a file of that name with other contents was taken")
	}
}
