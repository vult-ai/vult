package daemon

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

func protectedACL(path string, _ os.FileInfo) error {
	acl := exec.Command("/bin/ls", "-lde", path)
	acl.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	text, err := acl.Output()
	if err != nil {
		return errors.New("installation access controls could not be verified")
	}
	for _, line := range strings.Split(string(text), "\n")[1:] {
		if strings.Contains(line, " allow ") {
			return errors.New("unsupported installation access control list")
		}
	}
	return nil
}

func codeParents() []string {
	return []string{"/Library", "/Library/PrivilegedHelperTools", "/Library/PrivilegedHelperTools/ai.vult.ledger"}
}

func dataParents() []string {
	return []string{"/Library", "/Library/Application Support", "/Library/Application Support/ai.vult.ledger"}
}
