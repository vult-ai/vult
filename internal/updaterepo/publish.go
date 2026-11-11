package updaterepo

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/vult-ai/vult/internal/updatetrust"
)

// Freshen writes and signs a new snapshot naming the staged version of every
// targets role, and a new timestamp naming that snapshot. Production signs
// these with the online freshness keys; key files work the same way.
func (r Repo) Freshen(snapshotKey, timestampKey *Signer, now time.Time) error {
	snapshot := metadata.Snapshot(now.Add(validity[metadata.SNAPSHOT]).UTC())
	snapshot.Signed.Version = r.published(metadata.SNAPSHOT) + 1
	snapshot.Signed.Meta = map[string]*metadata.MetaFiles{}
	for _, role := range append([]string{metadata.TARGETS}, delegatedRoles...) {
		md, err := r.loadTargets(role)
		if err != nil {
			return err
		}
		snapshot.Signed.Meta[role+".json"] = metadata.MetaFile(md.Signed.Version)
	}
	if err := save(r.staged(metadata.SNAPSHOT), snapshot); err != nil {
		return err
	}
	always := func(string) bool { return true }
	if err := r.Sign(metadata.SNAPSHOT, snapshotKey, always); err != nil {
		return err
	}
	timestamp := metadata.Timestamp(now.Add(validity[metadata.TIMESTAMP]).UTC())
	timestamp.Signed.Version = r.published(metadata.TIMESTAMP) + 1
	timestamp.Signed.Meta = map[string]*metadata.MetaFiles{metadata.SNAPSHOT + ".json": metadata.MetaFile(snapshot.Signed.Version)}
	if err := save(r.staged(metadata.TIMESTAMP), timestamp); err != nil {
		return err
	}
	return r.Sign(metadata.TIMESTAMP, timestampKey, always)
}

// output is one file to write into published/metadata.
type output struct {
	name    string
	data    []byte
	replace bool // only timestamp.json is ever replaced
}

// prepare checks that every staged role is signed to its threshold and
// consistent, and returns the files a publish would write.
func (r Repo) prepare() ([]output, error) {
	root, err := load[metadata.RootType](r.staged(metadata.ROOT))
	if err != nil {
		return nil, err
	}
	if err := root.VerifyDelegate(metadata.ROOT, root); err != nil {
		return nil, fmt.Errorf("root is not signed by enough root keys: %w", err)
	}
	if p := r.published(metadata.ROOT); p > 0 && root.Signed.Version != p {
		if root.Signed.Version != p+1 {
			return nil, fmt.Errorf("root version %d does not follow published %d", root.Signed.Version, p)
		}
		prev, err := load[metadata.RootType](filepath.Join(r.metadataDir(), fmt.Sprintf("%d.root.json", p)))
		if err != nil {
			return nil, err
		}
		if err := prev.VerifyDelegate(metadata.ROOT, root); err != nil {
			return nil, fmt.Errorf("the new root is not signed by enough keys of the current root: %w", err)
		}
	}
	targets, err := r.loadTargets(metadata.TARGETS)
	if err != nil {
		return nil, err
	}
	if err := root.VerifyDelegate(metadata.TARGETS, targets); err != nil {
		return nil, fmt.Errorf("targets is not signed by enough targets keys: %w", err)
	}
	snapshot, err := load[metadata.SnapshotType](r.staged(metadata.SNAPSHOT))
	if err != nil {
		return nil, errors.New("no snapshot yet; run freshen first")
	}
	if err := root.VerifyDelegate(metadata.SNAPSHOT, snapshot); err != nil {
		return nil, fmt.Errorf("snapshot is not signed: %w", err)
	}
	timestamp, err := load[metadata.TimestampType](r.staged(metadata.TIMESTAMP))
	if err != nil {
		return nil, errors.New("no timestamp yet; run freshen first")
	}
	if err := root.VerifyDelegate(metadata.TIMESTAMP, timestamp); err != nil {
		return nil, fmt.Errorf("timestamp is not signed: %w", err)
	}
	if m := timestamp.Signed.Meta[metadata.SNAPSHOT+".json"]; m == nil || m.Version != snapshot.Signed.Version {
		return nil, errors.New("timestamp does not name the staged snapshot; run freshen again")
	}
	var out []output
	add := func(name string, data []byte, err error) error {
		if err != nil {
			return err
		}
		out = append(out, output{name: name, data: data})
		return nil
	}
	rootBytes, err := root.ToBytes(false)
	if err := add(fmt.Sprintf("%d.root.json", root.Signed.Version), rootBytes, err); err != nil {
		return nil, err
	}
	for _, role := range append([]string{metadata.TARGETS}, delegatedRoles...) {
		md, err := r.loadTargets(role)
		if err != nil {
			return nil, err
		}
		if role != metadata.TARGETS {
			if err := targets.VerifyDelegate(role, md); err != nil {
				return nil, fmt.Errorf("%s is not signed by its keys: %w", role, err)
			}
		}
		if m := snapshot.Signed.Meta[role+".json"]; m == nil || m.Version != md.Signed.Version {
			return nil, fmt.Errorf("snapshot does not name the staged %s; run freshen again", role)
		}
		data, err := md.ToBytes(false)
		if err := add(fmt.Sprintf("%d.%s.json", md.Signed.Version, role), data, err); err != nil {
			return nil, err
		}
	}
	data, err := snapshot.ToBytes(false)
	if err := add(fmt.Sprintf("%d.snapshot.json", snapshot.Signed.Version), data, err); err != nil {
		return nil, err
	}
	data, err = timestamp.ToBytes(false)
	if err != nil {
		return nil, err
	}
	out = append(out, output{name: "timestamp.json", data: data, replace: true})
	return out, nil
}

// Publish writes the staged metadata into published/ after proving that an
// installation would accept it: the result is first assembled in a scratch
// copy and every listed release is verified there exactly as an installation
// verifies it. Versioned files are never overwritten.
func (r Repo) Publish(now time.Time) error {
	files, err := r.prepare()
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp(r.Dir, ".publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	if err := copyTree(filepath.Join(r.Dir, "published"), scratch); err != nil {
		return err
	}
	write := func(dir string) error {
		for _, f := range files {
			file := filepath.Join(dir, f.name)
			existing, err := os.ReadFile(file)
			switch {
			case err == nil && bytes.Equal(existing, f.data):
				continue
			case err == nil && !f.replace:
				return fmt.Errorf("%s is already published with other content; versions never change once published", f.name)
			case err != nil && !errors.Is(err, os.ErrNotExist):
				return err
			}
			if f.replace {
				tmp := file + ".tmp"
				if err := os.WriteFile(tmp, f.data, 0o644); err != nil {
					return err
				}
				if err := os.Rename(tmp, file); err != nil {
					return err
				}
				continue
			}
			if err := writeNew(file, f.data, 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	if err := write(filepath.Join(scratch, "metadata")); err != nil {
		return err
	}
	if err := verifyAll(scratch, now); err != nil {
		return fmt.Errorf("not published: %w", err)
	}
	return write(r.metadataDir())
}

// verifyAll opens a published tree as an installation would, from its first
// root, and checks every listed release.
func verifyAll(published string, now time.Time) error {
	first, err := os.ReadFile(filepath.Join(published, "metadata", "1.root.json"))
	if err != nil {
		return err
	}
	cache, err := os.MkdirTemp("", "vult-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cache)
	c, err := updatetrust.Open(updatetrust.Config{InitialRoot: first, MetadataDir: cache, MetadataURL: localBase + "/metadata", TargetsURL: localBase + "/targets", Fetcher: dirFetcher{root: published}, Now: func() time.Time { return now }})
	if err != nil {
		return err
	}
	names, err := c.ReleaseNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err := c.Verify(name); err != nil && !errors.Is(err, updatetrust.ErrHeld) {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// Check is what `verify` reports: the newest installable release for a
// platform above installed, and why any newer listed release isn't.
func Check(published, platform, installed string, now time.Time) (*updatetrust.Release, []updatetrust.Refusal, error) {
	first, err := os.ReadFile(filepath.Join(published, "metadata", "1.root.json"))
	if err != nil {
		return nil, nil, err
	}
	cache, err := os.MkdirTemp("", "vult-verify-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(cache)
	c, err := updatetrust.Open(updatetrust.Config{InitialRoot: first, MetadataDir: cache, MetadataURL: localBase + "/metadata", TargetsURL: localBase + "/targets", Fetcher: dirFetcher{root: published}, Now: func() time.Time { return now }})
	if err != nil {
		return nil, nil, err
	}
	return c.Latest(platform, installed)
}

const localBase = "https://repository.invalid"

// dirFetcher serves a published tree to the verifier without a network.
type dirFetcher struct{ root string }

func (d dirFetcher) DownloadFile(url string, maxLength int64, _ time.Duration) ([]byte, error) {
	rel, ok := strings.CutPrefix(url, localBase+"/")
	if !ok || strings.Contains(rel, "..") {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: 404, URL: url}
	}
	f, err := os.Open(filepath.Join(d.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: 404, URL: url}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxLength+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxLength {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: "file exceeds its signed length"}
	}
	return data, nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// Summary describes a staged role in plain words: what signing it approves.
func (r Repo) Summary(role string) (string, error) {
	var b strings.Builder
	switch role {
	case metadata.ROOT:
		root, err := load[metadata.RootType](r.staged(role))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Root, version %d, valid until %s.\n", root.Signed.Version, root.Signed.Expires.Format("2 January 2006"))
		for _, name := range []string{metadata.ROOT, metadata.TARGETS, metadata.SNAPSHOT, metadata.TIMESTAMP} {
			rr := root.Signed.Roles[name]
			fmt.Fprintf(&b, "  %s: %d of %s\n", name, rr.Threshold, shortIDs(rr.KeyIDs))
		}
	case metadata.SNAPSHOT, metadata.TIMESTAMP:
		fmt.Fprintf(&b, "Freshness (%s).\n", role)
	default:
		md, err := r.loadTargets(role)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s, version %d, valid until %s.\n", role, md.Signed.Version, md.Signed.Expires.Format("2 January 2006"))
		if d := md.Signed.Delegations; d != nil {
			for _, dr := range d.Roles {
				fmt.Fprintf(&b, "  %s (%s): %d of %s\n", dr.Name, strings.Join(dr.Paths, ", "), dr.Threshold, shortIDs(dr.KeyIDs))
			}
		}
		previous := r.publishedTargets(role)
		names := make([]string, 0, len(md.Signed.Targets))
		for name := range md.Signed.Targets {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			t := md.Signed.Targets[name]
			label := "  "
			if _, ok := previous[name]; !ok {
				label = "  NEW "
			}
			c, err := contractOf(t)
			if err != nil {
				fmt.Fprintf(&b, "%s%s\n", label, name)
				continue
			}
			fmt.Fprintf(&b, "%sVult %s for %s\n", label, c.Version, c.Platform)
			fmt.Fprintf(&b, "      file      sha256 %s, %d bytes\n", hex.EncodeToString(t.Hashes["sha256"]), t.Length)
			fmt.Fprintf(&b, "      source    %s at %s (%s)\n", c.Source.Repository, c.Source.Commit, c.Source.Tag)
			fmt.Fprintf(&b, "      database  from %s to %s\n", strings.Join(c.SchemaFrom, ", "), c.SchemaTo)
			if c.MinimumOS != "" {
				fmt.Fprintf(&b, "      macOS     %s or later\n", c.MinimumOS)
			}
			if c.ServerImage != "" {
				fmt.Fprintf(&b, "      images    server %s, maintenance %s\n", c.ServerImage, c.MaintenanceImage)
			}
		}
		if role == updatetrust.RoleReleases && len(names) > 0 {
			if err := r.checkAgreement(); err == nil {
				b.WriteString("  The independent rebuild agrees with every release above.\n")
			}
		}
	}
	return b.String(), nil
}

func (r Repo) publishedTargets(role string) map[string]*metadata.TargetFiles {
	v := r.published(role)
	if v == 0 {
		return nil
	}
	md, err := load[metadata.TargetsType](filepath.Join(r.metadataDir(), fmt.Sprintf("%d.%s.json", v, role)))
	if err != nil {
		return nil
	}
	return md.Signed.Targets
}

func shortIDs(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = short(id)
	}
	return strings.Join(out, ", ")
}
