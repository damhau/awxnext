package resources

import (
	"math/rand"
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	a := splitLines("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\n")
	b := splitLines("one\n2\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\nthirteen")
	got, ok := unifiedDiff(a, b)
	want := `@@ -1,5 +1,5 @@
 one
-two
+2
 three
 four
 five
@@ -10,3 +10,4 @@
 ten
 eleven
 twelve
+thirteen
\ No newline at end of file
`
	if !ok || got != want {
		t.Fatalf("got:\n%s", got)
	}
	if d, _ := unifiedDiff(splitLines("x\n"), nil); d != "@@ -1 +0,0 @@\n-x\n" {
		t.Fatalf("removed: %q", d)
	}
	if d, _ := unifiedDiff(nil, splitLines("x\ny\n")); d != "@@ -0,0 +1,2 @@\n+x\n+y\n" {
		t.Fatalf("added: %q", d)
	}
}

// lineDiff's edits turn a into b, keeping every line of their longest
// common subsequence.
func TestLineDiffReplays(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	words := []string{"a\n", "b\n", "c\n", "d\n"}
	for i := 0; i < 500; i++ {
		var a, b []string
		for j := r.Intn(30); j > 0; j-- {
			a = append(a, words[r.Intn(len(words))])
		}
		for j := r.Intn(30); j > 0; j-- {
			b = append(b, words[r.Intn(len(words))])
		}
		ops, ok := lineDiff(a, b)
		if !ok {
			t.Fatal("too many edits")
		}
		var outA, outB []string
		same := 0
		for _, op := range ops {
			switch op.kind {
			case ' ':
				outA, outB = append(outA, a[op.a]), append(outB, b[op.b])
				same++
			case '-':
				outA = append(outA, a[op.a])
			case '+':
				outB = append(outB, b[op.b])
			}
		}
		if strings.Join(outA, "") != strings.Join(a, "") || strings.Join(outB, "") != strings.Join(b, "") {
			t.Fatalf("%q -> %q: %v", a, b, ops)
		}
		if same != lcs(a, b) {
			t.Fatalf("%q -> %q: kept %d lines, not %d", a, b, same, lcs(a, b))
		}
	}
}

func lcs(a, b []string) int {
	t := make([][]int, len(a)+1)
	for i := range t {
		t[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	return t[0][0]
}
