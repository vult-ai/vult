//go:build vult_updates

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/vult-ai/vult/internal/updatestatus"
)

// automaticUpdate handles `vult update` when this build has an update
// installer. `vult update status [--json]` reads only the updater's files, so it
// works when Vult can't start. It returns false for what it doesn't handle.
func automaticUpdate(_ context.Context, args []string, out io.Writer) (bool, error) {
	report := updatestatus.Read(updatestatus.Dir)
	if len(args) == 1 && args[0] == "undo" {
		return true, runUndo(context.Background(), out)
	}
	if len(args) >= 1 && args[0] == "status" {
		rest := args[1:]
		if len(rest) > 1 || len(rest) == 1 && rest[0] != "--json" && rest[0] != "--offline" {
			return true, fmt.Errorf("use vult update status [--json]")
		}
		if len(rest) == 1 && rest[0] == "--json" {
			return true, json.NewEncoder(out).Encode(report)
		}
		printReport(out, report)
		return true, nil
	}
	if len(args) == 0 && report.Enabled {
		printReport(out, report)
		fmt.Fprintln(out, "To update, press Update in Vult when a new version is offered. Details: vult update status")
		return true, nil
	}
	return false, nil
}

func printReport(out io.Writer, r updatestatus.Report) {
	fmt.Fprintln(out, "Updates:", r.Summary())
	if r.Installed != nil {
		fmt.Fprintf(out, "Installed: %s (database %s)\n", r.Installed.Version, strings.TrimSuffix(r.Installed.Schema, ".sql"))
	}
	if r.Fenced {
		fmt.Fprintln(out, "Writes are paused for an update.")
	}
	if r.LastOutcome != "" {
		line := "Last update: " + r.LastOutcome
		if r.LastReason != "" {
			line += " (" + r.LastReason + ")"
		}
		fmt.Fprintln(out, line)
	}
	for _, p := range r.Problems {
		fmt.Fprintln(out, "Problem:", p)
	}
	if n := len(r.History); n > 0 {
		fmt.Fprintln(out, "Recent history:")
		start := n - 5
		if start < 0 {
			start = 0
		}
		for _, e := range r.History[start:] {
			line := fmt.Sprintf("  %s  %s", e.Time.Local().Format("2006-01-02 15:04"), e.Phase)
			if e.Outcome != "" {
				line += " " + e.Outcome
			}
			if e.Available != "" {
				line += " " + e.Available
			}
			fmt.Fprintln(out, line)
		}
	}
}

// updateChecks is the updater's part of vult doctor.
func updateChecks() []check {
	r := updatestatus.Read(updatestatus.Dir)
	if !r.Enabled {
		return nil
	}
	c := check{Name: "Updates", OK: len(r.Problems) == 0, Detail: r.Summary()}
	if !c.OK {
		c.Detail = strings.Join(r.Problems, "; ")
		c.Remedy = "Run vult update status for details."
	}
	return []check{c}
}
