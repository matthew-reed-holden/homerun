package termsafe

import (
	"strings"
	"testing"
)

func TestLog(t *testing.T) {
	got := Log("a\x1b]52;c;aGk=\x07b\x1b[2J\x1b[Hc\rd\x1b[31me\x1b[0m\tf")
	if got != "abcde    f" {
		t.Fatalf("got %q", got)
	}
}

func TestCopy(t *testing.T) {
	var b strings.Builder
	in := "one\x1b]52;c;aGk=\x07\r\ntwo\x1b[1A\rthree\nlast"
	if err := Copy(&b, strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	if b.String() != "one\ntwothree\nlast\n" {
		t.Fatalf("got %q", b.String())
	}
}

func TestText(t *testing.T) {
	got := Text("a\x1b]52;c;aGk=\x07b\r\n\tc\x1b[2Jd\ne")
	if got != "ab\n    cd\ne" {
		t.Fatalf("got %q", got)
	}
	if got := Text("a\x1b]52;c;aGk=\x07\nb\tc\r"); got != "a\nb    c" {
		t.Fatalf("got %q", got)
	}
}
