package jobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestMaskForms(t *testing.T) {
	o := &Output{}
	o.Mask(`pa"ss\wörd`, "abc", "line one\nline-two\n  x\n")
	o.Mask("abc")
	e := o.masked(Event{
		Stdout:    `as given pa"ss\wörd, Go "pa\"ss\\wörd", Python "pa\"ss\\w\u00f6rd", twice "pa\\\"ss\\\\w\\u00f6rd", pin abc`,
		Task:      "line one",
		Play:      `json "line one\nline-two\n  x\n"`,
		EventData: map[string]any{"res": map[string]any{"msg": []any{"line-two", `pa"ss\wörd`, 4.0}, "line one": true}},
	})
	if want := `as given $encrypted$, Go "$encrypted$", Python "$encrypted$", twice "$encrypted$", pin abc`; e.Stdout != want {
		t.Errorf("stdout: %s", e.Stdout)
	}
	if e.Task != "$encrypted$" || e.Play != `json "$encrypted$"` {
		t.Errorf("task %q, play %q", e.Task, e.Play)
	}
	if got := fmt.Sprint(e.EventData); got != "map[res:map[$encrypted$:true msg:[$encrypted$ $encrypted$ 4]]]" {
		t.Errorf("event data: %s", got)
	}
}

func TestMaskLeavesTheRunnersDataAlone(t *testing.T) {
	o := &Output{}
	data := map[string]any{"artifact_data": map[string]any{"token": "s3cr3t-value"}}
	if e := o.masked(Event{EventData: data}); fmt.Sprint(e.EventData) != fmt.Sprint(data) {
		t.Errorf("masked without secrets: %v", e.EventData)
	}
	o.Mask("s3cr3t-value")
	e := o.masked(Event{EventData: data})
	if fmt.Sprint(e.EventData) != "map[artifact_data:map[token:$encrypted$]]" || data["artifact_data"].(map[string]any)["token"] != "s3cr3t-value" {
		t.Errorf("masked %v, original %v", e.EventData, data)
	}
}

func TestMaskLongestFirst(t *testing.T) {
	o := &Output{}
	o.Mask("secret", "secret-and-more")
	if e := o.masked(Event{Stdout: "secret-and-more secret"}); e.Stdout != "$encrypted$ $encrypted$" {
		t.Errorf("stdout: %s", e.Stdout)
	}
}

func TestMaskSecretsFromContext(t *testing.T) {
	MaskSecrets(context.Background(), "no output: nothing happens")
	o := &Output{}
	MaskSecrets(WithOutput(context.Background(), o), "from-the-context")
	if e := o.masked(Event{Stdout: "x from-the-context"}); e.Stdout != "x $encrypted$" {
		t.Errorf("stdout: %s", e.Stdout)
	}
}

func TestJSONEscape(t *testing.T) {
	for _, c := range []struct {
		in, utf, ascii string
	}{
		{"plain", "plain", "plain"},
		{"a\"b\\c\n\t\r\b\f\x01", `a\"b\\c\n\t\r\b\f\u0001`, `a\"b\\c\n\t\r\b\f\u0001`},
		{"é😀\x7f", "é😀\x7f", "\\u00e9\\ud83d\\ude00\\u007f"},
	} {
		if got := jsonEscape(c.in, false); got != c.utf {
			t.Errorf("jsonEscape(%q, false) = %q", c.in, got)
		}
		if got := jsonEscape(c.in, true); got != c.ascii {
			t.Errorf("jsonEscape(%q, true) = %q", c.in, got)
		}
	}
}

// benchEvents are events as a debug loop emits them: a runner_on_ok per
// item, one in a hundred printing a secret.
func benchEvents(n int, secret string) []Event {
	events := make([]Event, n)
	for i := range events {
		msg := fmt.Sprintf("event %d", i)
		if i%100 == 0 {
			msg += " " + secret
		}
		events[i] = Event{Event: "runner_on_ok", HostName: "localhost", Task: "ansible.builtin.debug", Play: "all",
			Stdout: fmt.Sprintf("ok: [localhost] => (item=%d) => {\n    \"msg\": %q\n}", i, msg),
			EventData: map[string]any{"host": "localhost", "task": "ansible.builtin.debug", "play": "all", "task_action": "ansible.builtin.debug",
				"task_uuid": "0242ac12-0002-6c3f-a1b2-000000000008", "play_pattern": "all", "playbook": "bench/playbooks/events.yml",
				"res": map[string]any{"msg": msg, "changed": false, "item": float64(i), "ansible_loop_var": "item", "_ansible_no_log": false,
					"_ansible_verbose_always": true, "_ansible_item_label": float64(i)}}}
	}
	return events
}

// BenchmarkMask is the cost of masking per event (ns/op), with the secrets
// of a job holding a few credentials, one a private key.
func BenchmarkMask(b *testing.B) {
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat(strings.Repeat("b3BlbnNzaC1rZXktdjEAAAAA", 3)[:70]+"\n", 25) + "-----END OPENSSH PRIVATE KEY-----\n"
	for _, secrets := range []int{0, 1, 20} {
		b.Run(fmt.Sprintf("secrets=%d", secrets), func(b *testing.B) {
			o := &Output{}
			for i := range secrets {
				o.Mask(fmt.Sprintf("Zq9-secret-value-%02d-x7Lm", i))
			}
			if secrets == 20 {
				o.Mask(key)
			}
			events := benchEvents(100_000, "Zq9-secret-value-00-x7Lm")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				o.masked(events[i%len(events)])
			}
		})
	}
}
