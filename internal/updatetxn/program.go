//go:build darwin || linux

package updatetxn

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/vult-ai/vult/internal/updatecheck"
)

// ReleaseMarker is the file the installer writes into a program directory
// it extracted, naming the release's SHA-256. Directories without it came
// from a package installer.
const ReleaseMarker = ".vult-release"

// ProgramDir is the live program directory. The release being installed is
// extracted beside it as Live+".next", swapped in, and the replaced program
// is kept as Live+".previous" until the next update.
type ProgramDir struct {
	Live string
	// Prefix, when set, selects the archive members under it (such as
	// "opt/vult/") as the program; others are left alone.
	Prefix string
}

func (d ProgramDir) next() string     { return d.Live + ".next" }
func (d ProgramDir) previous() string { return d.Live + ".previous" }

// Release returns the SHA-256 the directory's marker names, or "" if it has
// none.
func Release(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, ReleaseMarker))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func exists(p string) (bool, error) {
	_, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// settle finishes an install that stopped between the swap and moving the
// replaced program aside: if the live program is sha and the replaced one
// is still at .next, it moves it to .previous.
func (d ProgramDir) settle(sha string) error {
	live, err := Release(d.Live)
	if err != nil || live != sha {
		return err
	}
	ok, err := exists(d.next())
	if err != nil || !ok {
		return err
	}
	if err := os.RemoveAll(d.previous()); err != nil {
		return err
	}
	if err := os.Rename(d.next(), d.previous()); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(d.Live))
}

// Install makes the program in archive (already verified to have SHA-256
// sha) live. It is safe to call again at any point.
func (d ProgramDir) Install(archive, sha string) error {
	live, err := Release(d.Live)
	if err != nil {
		return err
	}
	if live == sha {
		return d.settle(sha)
	}
	if staged, err := Release(d.next()); err != nil || staged != sha {
		if err := os.RemoveAll(d.next()); err != nil {
			return err
		}
		if err := ExtractProgram(archive, d.next(), d.Prefix); err != nil {
			return err
		}
		if err := WriteDurably(filepath.Join(d.next(), ReleaseMarker), []byte(sha+"\n"), 0o644); err != nil {
			return err
		}
	}
	if err := SwapDirs(d.next(), d.Live); err != nil {
		return err
	}
	return d.settle(sha)
}

// Uninstall makes the program that sha replaced live again. It does nothing
// if sha isn't live. It is safe to call again at any point.
func (d ProgramDir) Uninstall(sha string) error {
	if err := d.settle(sha); err != nil {
		return err
	}
	live, err := Release(d.Live)
	if err != nil || live != sha {
		return err
	}
	ok, err := exists(d.previous())
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the previous program is missing")
	}
	return SwapDirs(d.previous(), d.Live)
}

// RemoveNext removes a half-extracted or replaced program left at .next.
func (d ProgramDir) RemoveNext() error { return os.RemoveAll(d.next()) }

// ExtractArchive unpacks a program archive into dest, which must not exist.
// It accepts only what updatecheck.ValidateArchive accepts, checks every
// entry again as it goes, and never follows a link.
func ExtractArchive(archive, dest string) error {
	if err := updatecheck.ValidateArchive(archive); err != nil {
		return err
	}
	return ExtractProgram(archive, dest, "")
}

// ExtractProgram unpacks the members of a tar or gzipped tar under prefix
// into dest, which must not exist. Every member of the archive, used or not,
// must be a plain file or directory at a clean relative path, named once,
// without set-id bits or write access for others; links are never followed.
func ExtractProgram(archive, dest, prefix string) error {
	if prefix != "" && (!strings.HasSuffix(prefix, "/") || path.Clean(prefix)+"/" != prefix || path.IsAbs(prefix)) {
		return fmt.Errorf("unsafe archive prefix %q", prefix)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	var source io.Reader = f
	magic := make([]byte, 2)
	if n, _ := io.ReadFull(f, magic); n == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("the program archive is damaged: %w", err)
		}
		defer gz.Close()
		source = gz
	} else if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(source)
	seen := map[string]bool{}
	used := 0
	for count := 0; ; count++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("the program archive is damaged: %w", err)
		}
		if count >= maxArchiveEntries {
			return errors.New("the program archive has too many entries")
		}
		full := strings.TrimSuffix(h.Name, "/")
		if full == "" || path.Clean(full) != full || path.IsAbs(full) || full == ".." || strings.HasPrefix(full, "../") || strings.ContainsRune(full, 0) || seen[full] {
			return fmt.Errorf("unsafe archive path %q", h.Name)
		}
		seen[full] = true
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return fmt.Errorf("archive entry %q is not a plain file or directory", full)
		}
		if h.Mode&(04000|02000|01000) != 0 || h.Mode&0o002 != 0 {
			return fmt.Errorf("archive entry %q has unsafe permissions", full)
		}
		name, ok := strings.CutPrefix(full, prefix)
		if prefix != "" && (!ok || name == "") {
			continue // outside the program, such as system files
		}
		if name == ReleaseMarker {
			return fmt.Errorf("unsafe archive path %q", h.Name)
		}
		used++
		mode := os.FileMode(h.Mode) & 0o755
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := makeDirs(dest, name); err != nil {
				return err
			}
			if err := os.Chmod(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := makeDirs(dest, path.Dir(name)); err != nil {
				return err
			}
			if err := writeEntry(target, tr, h.Size, mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %q is not a plain file or directory", name)
		}
	}
	if used == 0 {
		return errors.New("the program archive holds no program")
	}
	var directories []string
	if err := filepath.WalkDir(dest, func(name string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			directories = append(directories, name)
		}
		return err
	}); err != nil {
		return err
	}
	// A root-directory sync alone does not persist nested directory entries.
	for i := len(directories) - 1; i >= 0; i-- {
		if err := SyncDir(directories[i]); err != nil {
			return err
		}
	}
	return nil
}

const maxArchiveEntries = 20000

// makeDirs creates each missing directory of rel under dest with mode 0755,
// whatever the umask.
func makeDirs(dest, rel string) error {
	if rel == "." {
		return nil
	}
	dir := dest
	for _, part := range strings.Split(rel, "/") {
		dir = filepath.Join(dir, part)
		err := os.Mkdir(dir, 0o755)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(target string, r io.Reader, size int64, mode os.FileMode) error {
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	// Set the mode explicitly: the creating process's umask mustn't decide it.
	err = out.Chmod(mode)
	var n int64
	if err == nil {
		n, err = io.Copy(out, io.LimitReader(r, size))
	}
	if err == nil && n != size {
		err = io.ErrUnexpectedEOF
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
