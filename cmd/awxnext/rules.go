package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/damhau/awxnext/internal/rulestest"
)

// rulesCommand runs `awxnext rules test [-v] DIR`. Its exit status is 0
// when every test passes, 1 when one fails, and 2 when the directory's
// documents don't load or a rule's expressions don't compile.
func rulesCommand(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "test" {
		fmt.Fprintln(os.Stderr, "usage: awxnext rules test [-v] DIR")
		return 2
	}
	fs := flag.NewFlagSet("rules test", flag.ContinueOnError)
	verbose := fs.Bool("v", false, "list the tests that pass too")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: awxnext rules test [-v] DIR")
		return 2
	}
	results, err := rulestest.Run(ctx, fs.Arg(0))
	var lerr *rulestest.Error
	if errors.As(err, &lerr) {
		for _, p := range lerr.Problems {
			fmt.Fprintln(os.Stderr, p)
		}
		return 2
	} else if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if len(results) == 0 {
		fmt.Fprintln(os.Stderr, "no EventRuleTest document in", fs.Arg(0))
		return 2
	}
	if rulestest.Report(os.Stdout, results, *verbose) > 0 {
		return 1
	}
	return 0
}
