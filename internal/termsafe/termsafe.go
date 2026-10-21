// Package termsafe makes untrusted text safe to print on a terminal.
package termsafe

import (
	"bufio"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Line makes untrusted text safe for one terminal line. GitHub data and
// daemon output can carry escapes (OSC 52 clipboard writes, cursor movement)
// that would otherwise run on the user's terminal.
func Line(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

// Log is Line for log output, where a tab is common and stays readable.
func Log(s string) string {
	return Line(strings.ReplaceAll(s, "\t", "    "))
}

// Text is Line for multi-line text: each line cleaned, newlines kept, tabs
// shown as four spaces.
func Text(s string) string {
	ls := strings.Split(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\t", "    "), "\n")
	for i, l := range ls {
		ls[i] = Line(l)
	}
	return strings.Join(ls, "\n")
}

var sgr = regexp.MustCompile(`^\x1b\[[0-9;:]*m$`)

// Styled is for a renderer's output (markdown) built from untrusted text: it
// keeps SGR styling, printable text and newlines, and drops every other escape
// sequence (OSC, DCS, APC, cursor movement, C1 forms) and control character.
// A renderer can decode entities such as &#27; into escapes after Text ran.
func Styled(s string) string {
	var b strings.Builder
	var state byte
	for len(s) > 0 {
		seq, width, n, st := ansi.DecodeSequence(s, state, nil)
		state, s = st, s[n:]
		switch {
		case seq == "\n", sgr.MatchString(seq):
			b.WriteString(seq)
		case width > 0 || isText(seq):
			b.WriteString(Line(seq))
		}
	}
	return b.String()
}

// isText reports printable text: a grapheme without width (a combining mark
// or joiner) is still text, unlike an escape sequence or control character.
func isText(seq string) bool {
	r, _ := utf8.DecodeRuneInString(seq)
	return r != utf8.RuneError && !unicode.IsControl(r)
}

// Copy writes r to w one cleaned line at a time. Escapes are stripped per
// line, so one split across a line break cannot survive.
func Copy(w io.Writer, r io.Reader) error {
	br := bufio.NewReader(r)
	for {
		l, err := br.ReadString('\n')
		if l != "" {
			if _, werr := io.WriteString(w, Log(strings.TrimRight(l, "\r\n"))+"\n"); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
