package resources

import "testing"

func TestLoopbackURL(t *testing.T) {
	for listen, want := range map[string]string{
		":8052":          "http://127.0.0.1:8052",
		"0.0.0.0:8052":   "http://127.0.0.1:8052",
		"[::]:8052":      "http://127.0.0.1:8052",
		"10.0.0.5:8080":  "http://10.0.0.5:8080",
		"localhost:9000": "http://localhost:9000",
		"[::1]:8052":     "http://[::1]:8052",
		"nonsense":       "",
	} {
		if got := loopbackURL(listen); got != want {
			t.Errorf("%q: %q, want %q", listen, got, want)
		}
	}
}

func TestSentence(t *testing.T) {
	for in, want := range map[string]string{"": "", "boom": "boom.", "Done.": "Done.", "why? ": "why?"} {
		if got := sentence(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestLastLine(t *testing.T) {
	o := &connectorOutput{log: []byte("2026-11-15T23:55:03Z starting\n2026-11-15T23:55:03Z ansible.eda.range: no event_source plugin\n")}
	if got := o.lastLine(); got != "ansible.eda.range: no event_source plugin" {
		t.Errorf("runner line: %q", got)
	}
	o = &connectorOutput{log: []byte("Traceback (most recent call last):\nRuntimeError: boom\n")}
	if got := o.lastLine(); got != "RuntimeError: boom" {
		t.Errorf("traceback: %q", got)
	}
}
