//go:build vult_updates && (darwin || linux)

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vult-ai/vult/internal/updatehelper"
)

// runUndo puts back the version and data from before the last update, after
// the person confirms at the terminal that anything since is lost.
func runUndo(ctx context.Context, out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("run this with sudo: undoing an update needs an administrator")
	}
	l := updatehelper.DefaultLayout
	last, err := updatehelper.LastUpdate(l)
	if err != nil {
		return err
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return errors.New("undo needs an interactive terminal to confirm")
	}
	defer tty.Close()
	fmt.Fprintf(tty, "This puts back Vult %s and your Books exactly as they were before the update to %s.\n", last.Previous.Version, last.Next.Version)
	fmt.Fprintln(tty, "Anything recorded since that update will be lost. Vult stops for about a minute.")
	fmt.Fprint(tty, "Type undo to continue: ")
	answer, _ := bufio.NewReader(tty).ReadString('\n')
	if strings.TrimSpace(answer) != "undo" {
		return errors.New("nothing was changed")
	}
	installed, err := updatehelper.Undo(ctx, l, updatehelper.NewSystem(l), last.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Vult %s is running again, with your Books as they were before the update.\n", installed.Version)
	return nil
}
