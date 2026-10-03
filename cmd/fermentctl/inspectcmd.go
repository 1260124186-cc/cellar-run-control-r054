package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
)

type inspectFlags struct {
	quiet bool
}

func runInspectBackup(arguments []string, stdout, stderr io.Writer, stdin io.Reader) error {
	flags := flag.NewFlagSet("inspect-backup", flag.ContinueOnError)
	var values inspectFlags
	flags.BoolVar(&values.quiet, "quiet", false, "print only the checksum and integrity verdict")
	flags.BoolVar(&values.quiet, "q", false, "alias for --quiet")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: fermentctl inspect-backup [--quiet] <archive>")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("inspect-backup requires exactly one archive path")
	}
	archive, openErr := backup.OpenArchive(flags.Arg(0))
	if openErr != nil {
		return openErr
	}
	report := backup.Verify(archive)
	printInspection(stdout, report, values.quiet)
	if !report.Valid() {
		return errors.New("backup integrity check failed:\n  - " + strings.Join(report.Errors, "\n  - "))
	}
	return nil
}
