package workflow

import (
	"regexp"
	"slices"
	"strings"
)

var defaultPRTypes = []string{"opened", "synchronize", "reopened"}

func (w *Workflow) Matches(event, ref, action string, changed []string) bool {
	f, ok := w.On[event]
	if !ok {
		return false
	}
	if f == nil {
		f = &Filter{}
	}
	if event == "pull_request" {
		types := f.Types
		if len(types) == 0 {
			types = defaultPRTypes
		}
		if !slices.Contains(types, action) {
			return false
		}
	}
	// GitHub does not evaluate path filters for tag pushes.
	if strings.HasPrefix(ref, "refs/tags/") {
		return refOK(event, ref, f)
	}
	return refOK(event, ref, f) && pathsOK(f, changed)
}

func refOK(event, ref string, f *Filter) bool {
	if event == "pull_request" {
		return filterList(ref, f.Branches, f.BranchesIgnore)
	}
	hasBranch := f.Branches != nil || f.BranchesIgnore != nil
	hasTag := f.Tags != nil || f.TagsIgnore != nil
	if tag, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
		// GitHub: filtering only branches means tag pushes never match, and vice versa.
		if !hasTag {
			return !hasBranch
		}
		return filterList(tag, f.Tags, f.TagsIgnore)
	}
	if !hasBranch {
		return !hasTag
	}
	return filterList(strings.TrimPrefix(ref, "refs/heads/"), f.Branches, f.BranchesIgnore)
}

func filterList(name string, include, ignore []string) bool {
	if include != nil {
		return matchList(name, include)
	}
	if ignore != nil {
		return !matchList(name, ignore)
	}
	return true
}

func pathsOK(f *Filter, changed []string) bool {
	if changed == nil || (f.Paths == nil && f.PathsIgnore == nil) {
		return true
	}
	if f.Paths != nil {
		for _, c := range changed {
			if matchList(c, f.Paths) {
				return true
			}
		}
		return false
	}
	for _, c := range changed {
		if !matchList(c, f.PathsIgnore) {
			return true
		}
	}
	return false
}

// matchList applies patterns in order; a later "!pattern" can exclude an earlier match.
func matchList(name string, patterns []string) bool {
	matched := false
	for _, p := range patterns {
		if neg, ok := strings.CutPrefix(p, "!"); ok {
			if matchGlob(neg, name) {
				matched = false
			}
		} else if matchGlob(p, name) {
			matched = true
		}
	}
	return matched
}

// matchGlob implements GitHub's filter pattern syntax, where "**.md" matches at
// any depth (unlike shell globs) and ? and + quantify the preceding character.
// ponytail: compiles per call; cache compiled patterns if polling many repos shows it in a profile.
func matchGlob(pattern, name string) bool {
	re, err := compileGlob(pattern)
	return err == nil && re.MatchString(name)
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			switch {
			case strings.HasPrefix(pattern[i:], "**/"):
				b.WriteString("(.*/)?")
				i += 2
			case strings.HasPrefix(pattern[i:], "**"):
				b.WriteString(".*")
				i++
			default:
				b.WriteString("[^/]*")
			}
		case '?', '+':
			b.WriteByte(c)
		case '[':
			if j := strings.IndexByte(pattern[i:], ']'); j > 0 {
				b.WriteString(pattern[i : i+j+1])
				i += j
			} else {
				b.WriteString(`\[`)
			}
		case '\\':
			if i+1 < len(pattern) {
				b.WriteString(regexp.QuoteMeta(pattern[i+1 : i+2]))
				i++
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
