package local

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/vult-ai/vult/internal/fssecurity"
)

type DesktopSession struct {
	Home        string
	Environment []string
}

var localDisplay = regexp.MustCompile(`^:[0-9]{1,5}(\.[0-9]{1,2})?$`)
var waylandDisplay = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// CurrentDesktopSession uses the caller's actual account and local session bus.
// Loader, proxy, remote-display and caller-selected bus settings are not passed
// to browser or certificate tools.
func CurrentDesktopSession() (DesktopSession, error) {
	var session DesktopSession
	uid := os.Getuid()
	if uid == 0 || uid != os.Geteuid() {
		return session, errors.New("browser setup requires an unprivileged desktop session")
	}
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil || account.Username == ServiceUser || account.Username == BrowserUser || !filepath.IsAbs(account.HomeDir) || filepath.Clean(account.HomeDir) != account.HomeDir {
		return session, errors.New("desktop account is unavailable")
	}
	if data := os.Getenv("XDG_DATA_HOME"); data != "" && data != filepath.Join(account.HomeDir, ".local/share") {
		if _, err := os.Lstat(filepath.Join(account.HomeDir, ".pki/nssdb")); err != nil {
			return session, errors.New("local browser setup requires Chrome's standard certificate directory")
		}
	}
	for path := account.HomeDir; path != "/"; path = filepath.Dir(path) {
		owner := uint32(0)
		if path == account.HomeDir {
			owner = uint32(uid)
		}
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
		if err != nil {
			return session, errors.New("desktop home is unavailable")
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return session, errors.New("desktop home is unavailable")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		valid := ok && info.IsDir() && stat.Uid == owner && info.Mode().Perm()&0022 == 0 && fssecurity.NoExtendedACL(file)
		file.Close()
		if !valid {
			return session, errors.New("desktop home ownership or access controls are unsafe")
		}
	}
	runtime := "/run/user/" + strconv.Itoa(uid)
	info, err := os.Lstat(runtime)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return session, errors.New("private desktop session is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(uid) {
		return session, errors.New("desktop session has an unexpected owner")
	}
	bus, err := os.Lstat(filepath.Join(runtime, "bus"))
	if err != nil || bus.Mode()&os.ModeSocket == 0 {
		return session, errors.New("local desktop session bus is unavailable")
	}
	busStat, ok := bus.Sys().(*syscall.Stat_t)
	if !ok || busStat.Uid != uint32(uid) {
		return session, errors.New("desktop session bus has an unexpected owner")
	}
	env := []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "HOME=" + account.HomeDir,
		"XDG_RUNTIME_DIR=" + runtime, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtime + "/bus", "XDG_CURRENT_DESKTOP=GNOME"}
	display, wayland := os.Getenv("DISPLAY"), os.Getenv("WAYLAND_DISPLAY")
	if display != "" {
		if !localDisplay.MatchString(display) {
			return session, errors.New("a local desktop display is required")
		}
		env = append(env, "DISPLAY="+display)
	}
	if wayland != "" {
		if !waylandDisplay.MatchString(wayland) {
			return session, errors.New("a local desktop display is required")
		}
		info, err := os.Lstat(filepath.Join(runtime, wayland))
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			return session, errors.New("local desktop display is unavailable")
		}
		env = append(env, "WAYLAND_DISPLAY="+wayland, "XDG_SESSION_TYPE=wayland")
	} else if display == "" {
		return session, errors.New("browser setup requires a graphical desktop")
	} else {
		env = append(env, "XDG_SESSION_TYPE=x11")
	}
	if authority := os.Getenv("XAUTHORITY"); authority != "" {
		if !filepath.IsAbs(authority) || filepath.Clean(authority) != authority || !(strings.HasPrefix(authority, runtime+"/") || strings.HasPrefix(authority, account.HomeDir+"/")) {
			return session, errors.New("desktop display authentication is unavailable")
		}
		env = append(env, "XAUTHORITY="+authority)
	}
	return DesktopSession{Home: account.HomeDir, Environment: env}, nil
}
