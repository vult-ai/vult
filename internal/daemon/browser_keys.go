package daemon

import "github.com/vult-ai/vult/internal/browserkeys"

// The browser service's keyring folder, prepared by the installer and every
// update and checked by the service.
func browserKeyFolder(base string) string { return browserkeys.Folder(base) }

func prepareBrowserKeys(base string, uid, gid uint32) error {
	return browserkeys.Prepare(base, uid, gid)
}

func checkBrowserKeys(base string, uid uint32) error { return browserkeys.Check(base, uid) }
