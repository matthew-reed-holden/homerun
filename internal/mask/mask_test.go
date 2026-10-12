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

func TestMaskMultiLineSecretLines(t *testing.T) {
	pem := "-----BEGIN KEY-----\nMIIEabcdefghijklmnop\nqrstuvwxyz0123456789\nAQAB==\n-----END KEY-----"
	m := New([]string{pem})
	cases := map[string]string{
		"qrstuvwxyz0123456789": "***",
		"-----BEGIN KEY-----":  "-----BEGIN KEY-----",
		"AQAB==":               "***",
		"{":                    "{",
		"x: AQAB== y":          "x: *** y",
	}
	for in, want := range cases {
		if got := m.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
	jsonSecret := "{\n  \"k\": \"v1234\"\n}"
	jm := New([]string{jsonSecret})
	if got := jm.Mask("{"); got != "{" {
		t.Errorf("Mask(\"{\") = %q, want \"{\"", got)
	}
}

func TestMaskOverlappingSecrets(t *testing.T) {
	m := New([]string{"abcdef12", "zzabc"})
	if got := m.Mask("zzabcdef12"); got != "***" {
		t.Errorf("Mask(overlap) = %q, want \"***\"", got)
	}
	if got := m.Mask("zzabc and abcdef12 apart"); got != "*** and *** apart" {
		t.Errorf("Mask(adjacent) = %q", got)
	}
}
