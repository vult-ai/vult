package recovery

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Clone copies the directory src to dst, which must not exist, keeping each
// file's permissions and owner. The data is stopped while this runs. Links
// and special files are refused: a data directory has neither.
func Clone(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	var directories []struct {
		path string
		info fs.FileInfo
	}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if err := os.Mkdir(target, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			directories = append(directories, struct {
				path string
				info fs.FileInfo
			}{target, info})
		case info.Mode().IsRegular():
			if err := copyFile(path, target, info); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s is not a plain file or directory", rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Apply directory metadata after filling children, then persist every
	// directory from the leaves up before making the copy's root durable.
	for i := len(directories) - 1; i >= 0; i-- {
		dir := directories[i]
		f, err := os.Open(dir.path)
		if err != nil {
			return err
		}
		err = keepOwner(dir.path, dir.info)
		if err == nil {
			err = f.Chmod(dir.info.Mode().Perm())
		}
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	d, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer d.Close()
	return FullSync(d)
}

func copyFile(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, info.Mode().Perm()|0o200)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = keepOwner(dst, info)
	}
	if err == nil {
		err = out.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

func keepOwner(path string, info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("file owner unavailable")
	}
	if os.Geteuid() != 0 {
		return nil // only root may give files away; tests run as a user
	}
	return os.Lchown(path, int(st.Uid), int(st.Gid))
}

// FullSync writes f through to stable storage.
func FullSync(f *os.File) error { return f.Sync() }
