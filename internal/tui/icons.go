package tui

import "strings"

// IconMode picks the glyph set: Nerd Font octicons, plain Unicode, or ASCII.
type IconMode int

const (
	IconsNerd IconMode = iota
	IconsUnicode
	IconsASCII
)

// ParseIcons reads the icons: setting; empty means nerd, unknown is reported.
func ParseIcons(s string) (IconMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "nerd":
		return IconsNerd, true
	case "unicode":
		return IconsUnicode, true
	case "ascii":
		return IconsASCII, true
	}
	return IconsNerd, false
}

var icons = IconsNerd

// ic returns the glyph for a role in the current mode; "?" for an unknown role.
func ic(role string) string {
	g, ok := iconTable[role]
	if !ok {
		return "?"
	}
	return g[icons]
}

// iconTable maps a role to {nerd, unicode, ascii}; mirrors docs/design/tui-reference.html.
var iconTable = map[string][3]string{
	"pr-open":      {"\U0000f407", "●", "o"},
	"pr-draft":     {"\U0000f4dd", "◌", "d"},
	"pr-merged":    {"\U0000f419", "◆", "m"},
	"pr-closed":    {"\U0000f4dc", "⊘", "c"},
	"ci-pass":      {"\U0000f49e", "✓", "+"},
	"ci-fail":      {"\U0000f52f", "✗", "x"},
	"ci-run":       {"\U0000f46a", "⟳", "*"},
	"ci-pending":   {"\U0000f444", "•", "."},
	"ci-skip":      {"\U0000f517", "○", "-"},
	"checks":       {"\U0000f45e", "▤", "#"},
	"rv-approved":  {"\U0000f42e", "✔", "A"},
	"rv-changes":   {"\U0000f4d2", "±", "C"},
	"rv-commented": {"\U0000f41f", "✎", "c"},
	"rv-none":      {"\U0000f4c3", "·", "."},
	"comments":     {"\U0000f442", "≡", "="},
	"diff":         {"\U0000f440", "±", "~"},
	"age":          {"\U0000f43a", "◷", "t"},
	"behind":       {"\U0000f433", "↓", "v"},
	"branch":       {"\U0000f418", "⎇", "b"},
	"commit":       {"\U0000f417", "◦", "o"},
	"run":          {"\U0000f500", "▶", ">"},
	"overview":     {"\U0000f449", "ⓘ", "i"},
	"src-homerun":  {"\U000f0852", "◉", "h"},
	"src-actions":  {"\U0000f408", "◇", "g"},
	"warn":         {"\U0000f421", "▲", "!"},
	"viewed":       {"\U0000f441", "✓", "v"},
	"search":       {"\U0000f422", "⌕", "/"},
	"help":         {"\U0000f420", "?", "?"},
	"repo":         {"\U0000f401", "▣", "r"},
	"user":         {"\U0000f415", "@", "@"},
	"engine":       {"\U0000f473", "◈", "e"},
	"config":       {"\U0000f423", "✱", "*"},
	"doctor":       {"\U0000f510", "✚", "+"},
	"score":        {"\U000f15ec", "⋄", "S"},
}
