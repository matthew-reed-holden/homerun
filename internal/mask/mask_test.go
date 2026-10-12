package mask

import "testing"

func TestMask(t *testing.T) {
	pem := "-----BEGIN KEY-----\nMIIEabcdefghijklmnop\nqrstuvwxyz0123456789\n-----END KEY-----"
	m := New([]string{"topsecret", "top", "", pem})
	cases := map[string]string{
		"echo topsecret":                 "echo ***",
		"a top b":                        "a *** b",
		"line: MIIEabcdefghijklmnop end": "line: *** end",
		"nothing here":                   "nothing here",
	}
	for in, want := range cases {
		if got := m.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
	var nilM *Masker
	if nilM.Mask("x") != "x" {
		t.Error("nil masker must be identity")
	}
}
