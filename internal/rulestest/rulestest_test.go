package rulestest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExamplesPass(t *testing.T) {
	results, err := Run(context.Background(), "../../docs/examples/rules")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if failed := Report(&out, results, true); failed != 0 || len(results) != 4 {
		t.Fatalf("%d of %d failed:\n%s", failed, len(results), out.String())
	}
}

func TestFailures(t *testing.T) {
	results, err := Run(context.Background(), "testdata/failing")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || len(results[0].Failures) != 0 {
		t.Fatalf("results %+v", results)
	}
	got := results[1].Failures
	want := []string{
		`event 1 (e1, at +0s): want fired, got awaiting_approval, key "db1": The rule is in approval mode.`,
		`event 2 (e2, at +1m0s): not_matched as expected, but reason "The filter's condition event.data.labels.alertname == \"DiskFull\" is false, with event.data.labels.alertname = \"CPU\"." does not contain "event.type"`,
		`event 3 (e3, at +2m0s): the host_lock guardrail needs the jobs running on the server: awxnext rules test doesn't check it`,
		`event 4 (e4, at +3m0s): awaiting_approval as expected, but key "db3", want "db4"; message "Disk full on db1", want "Disk full on db4"`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("failures:\n%s", strings.Join(got, "\n"))
	}
	if len(results[1].Notes) != 1 || !strings.Contains(results[1].Notes[0], "window Night covers inventory groups") {
		t.Fatalf("notes %q", results[1].Notes)
	}
	var out bytes.Buffer
	if Report(&out, results, false) != 1 || strings.Contains(out.String(), "Batches a window") {
		t.Fatalf("report:\n%s", out.String())
	}
}

func TestBrokenDirectory(t *testing.T) {
	_, err := Run(context.Background(), "testdata/broken")
	var lerr *Error
	if !errors.As(err, &lerr) {
		t.Fatalf("err %v", err)
	}
	all := strings.Join(lerr.Problems, "\n")
	for _, want := range []string{`no EventRule named "Missing"`, `EventRule Broken: filter:`, `found no matching overload for '_>_'`} {
		if !strings.Contains(all, want) {
			t.Errorf("problems lack %q:\n%s", want, all)
		}
	}
	if _, err := Run(context.Background(), "testdata/none"); err == nil {
		t.Error("a missing directory loaded")
	}
}
