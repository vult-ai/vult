// Command vult-release maintains Vult's signed update repository: offline
// keys as encrypted files, adding releases and the rebuild's agreement,
// holds, signing, freshness and publishing. It never uploads anything; the
// published/ directory it produces is copied to the update host separately.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"golang.org/x/term"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/updaterepo"
	"github.com/vult-ai/vult/internal/updatetrust"
)

const usage = `vult-release maintains Vult's signed update repository.

  key new --out FILE                     create an encrypted key file (and FILE.pub)
  key test --key FILE                    restore test: open, sign and verify with a key file
  key touchid --out FILE                 create a Touch ID key in this Mac's Secure Enclave (and FILE.pub)
  init --repo DIR --root-keys A,B,C --targets-keys A,B,C
       --release-keys P,Q --rebuild-key P --snapshot-key P --timestamp-key P
                                         start a repository from public key files
  release add --repo DIR --artifact FILE --contract FILE
  rebuild add --repo DIR --artifact FILE --contract FILE
  hold --repo DIR --version VERSION
  show --repo DIR --role ROLE            what signing ROLE would approve
  sign --repo DIR --role ROLE (--key FILE | --touchid FILE)
  freshen --repo DIR --snapshot-key FILE --timestamp-key FILE
  publish --repo DIR
  verify --repo DIR --platform PLATFORM --installed VERSION

The automatic signers (the rebuild's agreement, and freshen) may take the
passphrase from an environment variable with --passphrase-env NAME, and sign
the rebuild's agreement with --unattended. Nothing else can.

A release can only be signed after the rebuild's agreement is signed. Key
files are decrypted in memory for one step and never written decrypted.
`

// prompt is how the command asks the person at the keyboard.
type prompt interface {
	Passphrase(label string, twice bool) ([]byte, error)
	Confirm(summary string) bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout, ttyPrompt{}, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "vult-release:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, p prompt, now func() time.Time) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	if (cmd == "key" || cmd == "release" || cmd == "rebuild") && len(args) > 0 {
		cmd, args = cmd+" "+args[0], args[1:]
	}
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	s := func(name string) *string { return f.String(name, "", "") }
	repo, out1, key, role, artifact, contractFile, version := s("repo"), s("out"), s("key"), s("role"), s("artifact"), s("contract"), s("version")
	rootKeys, targetsKeys, releaseKeys, rebuildKey, snapshotKey, timestampKey := s("root-keys"), s("targets-keys"), s("release-keys"), s("rebuild-key"), s("snapshot-key"), s("timestamp-key")
	touchID, touchIDProgram := s("touchid"), f.String("touchid-program", updaterepo.TouchIDProgram, "")
	platform, installed := s("platform"), s("installed")
	passEnv := s("passphrase-env")
	unattended := f.Bool("unattended", false, "")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return errors.New("invalid arguments; run vult-release help")
	}
	if *passEnv != "" || *unattended {
		// Only the automatic signers run without a person: the rebuild's
		// agreement and freshness. Releases, holds and the root keys are
		// always signed by someone at the keyboard.
		automatic := cmd == "freshen" || cmd == "key new" || cmd == "key test" || cmd == "sign" && *role == updatetrust.RoleRebuild
		if !automatic || *passEnv == "" {
			return errors.New("--passphrase-env and --unattended are only for the rebuild's agreement and freshness signing")
		}
		p = envPrompt{name: *passEnv, confirm: *unattended}
	}
	need := func(values ...*string) error {
		for _, v := range values {
			if *v == "" {
				return errors.New("missing a required option; run vult-release help")
			}
		}
		return nil
	}
	r := updaterepo.Repo{Dir: *repo}
	switch cmd {
	case "key new":
		if err := need(out1); err != nil {
			return err
		}
		pass, err := p.Passphrase("Passphrase for the new key file", true)
		if err != nil {
			return err
		}
		pub, id, err := updaterepo.NewKey(*out1, pass)
		if err != nil {
			return err
		}
		if err := updaterepo.WritePublicKey(*out1+".pub", pub); err != nil {
			return err
		}
		fmt.Fprintf(out, "Created %s and %s.pub\nKey ID %s\n", *out1, *out1, id)
	case "key test":
		if err := need(key); err != nil {
			return err
		}
		pass, err := p.Passphrase("Passphrase for "+*key, false)
		if err != nil {
			return err
		}
		id, err := updaterepo.TestKey(*key, pass)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Restore test passed. Key ID %s\n", id)
	case "key touchid":
		if err := need(out1); err != nil {
			return err
		}
		if err := runProgram(*touchIDProgram, "create", *out1); err != nil {
			return err
		}
		_, id, err := updaterepo.ReadPublicKey(*out1 + ".pub")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Created a Touch ID key: %s (usable only on this Mac) and %s.pub\nKey ID %s\n", *out1, *out1, id)
	case "init":
		if err := need(repo, rootKeys, targetsKeys, releaseKeys, rebuildKey, snapshotKey, timestampKey); err != nil {
			return err
		}
		var k updaterepo.Keys
		var err error
		if k.Root, err = publicKeys(*rootKeys); err != nil {
			return err
		}
		if k.Targets, err = publicKeys(*targetsKeys); err != nil {
			return err
		}
		if k.Release, err = publicKeys(*releaseKeys); err != nil {
			return err
		}
		for dst, file := range map[**metadata.Key]string{&k.Rebuild: *rebuildKey, &k.Snapshot: *snapshotKey, &k.Timestamp: *timestampKey} {
			if *dst, _, err = updaterepo.ReadPublicKey(file); err != nil {
				return err
			}
		}
		if _, err := updaterepo.Init(*repo, k, now()); err != nil {
			return err
		}
		fmt.Fprintf(out, "Started %s. Sign root, targets and every role, then freshen and publish.\n", *repo)
	case "release add", "rebuild add":
		if err := need(repo, artifact, contractFile); err != nil {
			return err
		}
		c, err := readContract(*contractFile)
		if err != nil {
			return err
		}
		add, next := r.AddRelease, updatetrust.RoleReleases
		if cmd == "rebuild add" {
			add, next = r.AddRebuild, updatetrust.RoleRebuild
		}
		name, err := add(*artifact, c, now())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Listed %s. Sign the %s role next.\n", name, next)
	case "hold":
		if err := need(repo, version); err != nil {
			return err
		}
		if err := r.Hold(*version, now()); err != nil {
			return err
		}
		fmt.Fprintf(out, "Held %s. Sign the holds role, then freshen and publish.\n", *version)
	case "show":
		if err := need(repo, role); err != nil {
			return err
		}
		summary, err := r.Summary(*role)
		if err != nil {
			return err
		}
		fmt.Fprint(out, summary)
	case "sign":
		if err := need(repo, role); err != nil {
			return err
		}
		if (*key == "") == (*touchID == "") {
			return errors.New("sign with exactly one of --key or --touchid")
		}
		var signer *updaterepo.Signer
		var err error
		if *touchID != "" {
			signer, err = updaterepo.OpenTouchID(*touchIDProgram, *touchID, "approve signing "+*role+" for Vult")
		} else {
			var pass []byte
			if pass, err = p.Passphrase("Passphrase for "+*key, false); err == nil {
				signer, err = updaterepo.OpenKey(*key, pass)
			}
		}
		if err != nil {
			return err
		}
		defer signer.Close()
		if err := r.Sign(*role, signer, p.Confirm); err != nil {
			return err
		}
		fmt.Fprintf(out, "Signed %s with key %s.\n", *role, signer.ID)
	case "freshen":
		if err := need(repo, snapshotKey, timestampKey); err != nil {
			return err
		}
		var signers []*updaterepo.Signer
		for _, file := range []string{*snapshotKey, *timestampKey} {
			pass, err := p.Passphrase("Passphrase for "+file, false)
			if err != nil {
				return err
			}
			s, err := updaterepo.OpenKey(file, pass)
			if err != nil {
				return err
			}
			defer s.Close()
			signers = append(signers, s)
		}
		if err := r.Freshen(signers[0], signers[1], now()); err != nil {
			return err
		}
		fmt.Fprintln(out, "Snapshot and timestamp signed.")
	case "publish":
		if err := need(repo); err != nil {
			return err
		}
		if err := r.Publish(now()); err != nil {
			return err
		}
		fmt.Fprintln(out, "Published. Every listed release verifies as an installation would check it.")
	case "verify":
		if err := need(repo, platform, installed); err != nil {
			return err
		}
		got, refused, err := updaterepo.Check(*repo+"/published", *platform, *installed, now())
		if err != nil {
			return err
		}
		for _, x := range refused {
			fmt.Fprintf(out, "Not offered: %s (%v)\n", x.Name, x.Err)
		}
		if got == nil {
			fmt.Fprintf(out, "Nothing newer than %s for %s.\n", *installed, *platform)
		} else {
			fmt.Fprintf(out, "An installation on %s %s would install %s (sha256 %s).\n", *platform, *installed, got.Contract.Version, got.SHA256)
		}
	default:
		return fmt.Errorf("unknown command %q; run vult-release help", cmd)
	}
	return nil
}

func publicKeys(list string) ([]*metadata.Key, error) {
	var keys []*metadata.Key
	for _, file := range strings.Split(list, ",") {
		k, _, err := updaterepo.ReadPublicKey(strings.TrimSpace(file))
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func readContract(file string) (updatetrust.Contract, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return updatetrust.Contract{}, err
	}
	var c updatetrust.Contract
	if err := strictjson.DecodeNonNull(data, &c, ""); err != nil {
		return updatetrust.Contract{}, fmt.Errorf("%s is not a release contract", file)
	}
	return c, c.Validate()
}

// ttyPrompt asks on the controlling terminal, never on piped input.
type ttyPrompt struct{}

func (ttyPrompt) open() (*os.File, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("signing needs a person at an interactive terminal")
	}
	return tty, nil
}

func (t ttyPrompt) Passphrase(label string, twice bool) ([]byte, error) {
	tty, err := t.open()
	if err != nil {
		return nil, err
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s: ", label)
	first, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	if err != nil || !twice {
		return first, err
	}
	fmt.Fprint(tty, "Again: ")
	second, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	if err != nil {
		return nil, err
	}
	if string(first) != string(second) {
		return nil, errors.New("the passphrases differ")
	}
	return first, nil
}

func (t ttyPrompt) Confirm(summary string) bool {
	tty, err := t.open()
	if err != nil {
		return false
	}
	defer tty.Close()
	fmt.Fprintf(tty, "\n%s\nSign exactly this? Type yes to sign: ", summary)
	line, _ := bufio.NewReader(tty).ReadString('\n')
	return strings.TrimSpace(line) == "yes"
}

func runProgram(program string, args ...string) error {
	cmd := exec.Command(program, args...)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", program, strings.TrimSpace(errOut.String()))
	}
	return nil
}

// envPrompt takes the passphrase from an environment variable, for the
// automatic signers that run without a person. It confirms only when asked
// to run unattended.
type envPrompt struct {
	name    string
	confirm bool
}

func (e envPrompt) Passphrase(string, bool) ([]byte, error) {
	value := os.Getenv(e.name)
	if value == "" {
		return nil, fmt.Errorf("%s is empty", e.name)
	}
	return []byte(value), nil
}

func (e envPrompt) Confirm(string) bool { return e.confirm }
