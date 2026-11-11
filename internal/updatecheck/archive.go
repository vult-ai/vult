// Package updatecheck validates release archives and operating system versions
// before installation.
package updatecheck

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

// ErrRefused means the program archive is unsafe to install.
var ErrRefused = errors.New("refused")

const maxArchiveEntries = 20000

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// ValidateArchive refuses a program archive with anything but plain files and
// directories at clean relative paths, without set-id bits or write access
// for others, each named once.
func ValidateArchive(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	seen := map[string]bool{}
	for count := 0; ; count++ {
		h, err := tr.Next()
		if err == io.EOF {
			if count == 0 {
				return refuse("the program archive is empty")
			}
			return nil
		}
		if err != nil {
			return refuse("the program archive is damaged")
		}
		if count >= maxArchiveEntries {
			return refuse("the program archive has too many entries")
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\x00") {
			return refuse("the program archive has an unsafe path %q", h.Name)
		}
		if seen[name] {
			return refuse("the program archive names %q twice", name)
		}
		seen[name] = true
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		default:
			return refuse("the program archive contains a link or special file %q", name)
		}
		if h.Mode&(04000|02000|01000) != 0 || h.Mode&0o002 != 0 {
			return refuse("the program archive gives %q unsafe permissions", name)
		}
	}
}

// AtLeast compares dotted numeric versions such as "26.1" and "26.0".
func AtLeast(have, need string) bool { return atLeast(have, need) }

func atLeast(have, need string) bool {
	a, b := strings.Split(have, "."), strings.Split(need, ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		var err error
		if i < len(a) {
			if x, err = strconv.Atoi(a[i]); err != nil {
				return false
			}
		}
		if i < len(b) {
			if y, err = strconv.Atoi(b[i]); err != nil {
				return false
			}
		}
		if x != y {
			return x > y
		}
	}
	return true
}
