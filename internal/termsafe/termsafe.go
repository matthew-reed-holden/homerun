// Package termsafe makes untrusted text safe to print on a terminal.
package termsafe

import (
	"bufio"
	"io"
	"strings"
	"unicode"

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
