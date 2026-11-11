// Package updatehelper is the privileged half of macOS updates: it installs
// a release the checker downloaded, as one journaled transaction, after
// verifying it again with its own trust store. It runs as root, started only
// by launchd, and takes no input but its own files and the checker's spool.
package updatehelper
