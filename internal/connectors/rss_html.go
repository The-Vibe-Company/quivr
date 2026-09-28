package connectors

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// htmlToText reduces feed HTML to plain text: scripts, styles and embedded
// objects are dropped, block elements become line breaks, entities are
// decoded and whitespace is collapsed. It reports whether the input carried
// markup, so the caller preserves the original only when it differs.
func htmlToText(s string) (string, bool) {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	markup := false
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return collapse(b.String()), markup
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			markup = true
			tt := z.Token()
			switch tt.DataAtom {
			case atom.Script, atom.Style, atom.Iframe, atom.Noscript, atom.Template, atom.Object, atom.Svg, atom.Head:
				if tt.Type == html.StartTagToken {
					skip++
				} else if tt.Type == html.EndTagToken && skip > 0 {
					skip--
				}
			case atom.Br, atom.P, atom.Div, atom.Li, atom.Ul, atom.Ol, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
				atom.Tr, atom.Table, atom.Blockquote, atom.Pre, atom.Section, atom.Article, atom.Hr, atom.Figure, atom.Figcaption, atom.Dd, atom.Dt:
				b.WriteByte('\n')
			}
		case html.CommentToken, html.DoctypeToken:
			markup = true
		}
	}
}

// collapse trims each line, collapses inner whitespace and drops empty lines
// and characters the ingestion path refuses.
func collapse(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, line := range lines {
		line = strings.Join(strings.FieldsFunc(line, func(r rune) bool { return unicode.IsSpace(r) || r == 0 || r == unicode.ReplacementChar }), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
