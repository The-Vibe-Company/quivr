// Package reference renders generated reference pages as GitHub Markdown.
//
// It is the source-agnostic core shared by every reference generator: an
// adapter (for example the OpenAPI adapter in the openapi subpackage) walks
// its source and writes headings, paragraphs, tables and code blocks to a
// Page. The core owns Markdown escaping and heading anchors, so every adapter
// links and escapes the same way. cmd/quivr-reference lists the pages and
// checks that the committed output is fresh.
package reference

import (
	"fmt"
	"regexp"
	"strings"
)

// Page accumulates one generated Markdown page.
type Page struct {
	b       strings.Builder
	anchors map[string]bool
	err     error
}

// NewPage starts a page with its title and a notice naming the source a
// reader must change instead of editing the page.
func NewPage(title, source string) *Page {
	p := &Page{anchors: map[string]bool{}}
	fmt.Fprintf(&p.b, "<!-- Generated from %s by `make generate`. Do not edit. -->\n\n", source)
	p.Heading(1, title)
	p.Para(fmt.Sprintf("> Generated from %s by `make generate`. Do not edit this page: change the source and regenerate.", Code(source)))
	return p
}

// Heading writes a heading and returns its GitHub anchor. Two headings with
// the same anchor would make links ambiguous, so the page then fails to render.
func (p *Page) Heading(level int, text string) string {
	anchor := Anchor(text)
	if p.anchors[anchor] {
		p.Fail(fmt.Errorf("duplicate heading anchor %q from %q", anchor, text))
	}
	p.anchors[anchor] = true
	fmt.Fprintf(&p.b, "%s %s\n\n", strings.Repeat("#", level), text)
	return anchor
}

// Para writes a Markdown paragraph, trimmed; an empty paragraph is skipped.
func (p *Page) Para(md string) {
	if md = strings.TrimSpace(md); md != "" {
		p.b.WriteString(md + "\n\n")
	}
}

// Table writes a table; cells are escaped with Cell. A table without rows is
// skipped.
func (p *Page) Table(header []string, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	line := func(cells []string) {
		escaped := make([]string, len(cells))
		for i, c := range cells {
			escaped[i] = Cell(c)
		}
		p.b.WriteString("| " + strings.Join(escaped, " | ") + " |\n")
	}
	line(header)
	sep := make([]string, len(header))
	for i := range sep {
		sep[i] = "---"
	}
	p.b.WriteString("| " + strings.Join(sep, " | ") + " |\n")
	for _, r := range rows {
		line(r)
	}
	p.b.WriteString("\n")
}

// CodeBlock writes a fenced code block long enough to contain any fence in body.
func (p *Page) CodeBlock(lang, body string) {
	fence := "```"
	for strings.Contains(body, fence) {
		fence += "`"
	}
	fmt.Fprintf(&p.b, "%s%s\n%s\n%s\n\n", fence, lang, strings.TrimRight(body, "\n"), fence)
}

// Details writes a collapsed section whose content is written by body.
func (p *Page) Details(summary string, body func()) {
	fmt.Fprintf(&p.b, "<details>\n<summary>%s</summary>\n\n", summary)
	body()
	p.b.WriteString("</details>\n\n")
}

// Fail records an error that makes Bytes fail; the first error wins.
func (p *Page) Fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

// Bytes returns the page, ending with a single newline, or the first error.
func (p *Page) Bytes() ([]byte, error) {
	if p.err != nil {
		return nil, p.err
	}
	return []byte(strings.TrimRight(p.b.String(), "\n") + "\n"), nil
}

var notSlug = regexp.MustCompile(`[^\p{L}\p{N} _-]`)

// Anchor is the anchor GitHub derives from heading text.
func Anchor(text string) string {
	s := notSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(text)), "")
	return strings.ReplaceAll(s, " ", "-")
}

// Code is an inline code span holding s; line breaks become spaces, as
// Markdown renders them inside a span anyway.
func Code(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

// Link is a link to an anchor on the same page.
func Link(text, anchor string) string { return "[" + text + "](#" + anchor + ")" }

// Cell turns Markdown into one table cell: pipes are escaped and line breaks
// become <br>, keeping paragraph and list breaks visible.
func Cell(md string) string {
	md = strings.ReplaceAll(strings.TrimSpace(md), "|", `\|`)
	var out strings.Builder
	for i, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		broken := strings.HasSuffix(out.String(), "<br>")
		switch {
		case i == 0:
		case line == "":
			if !broken {
				out.WriteString("<br><br>")
			}
			continue
		case broken:
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
			out.WriteString("<br>")
		default:
			out.WriteString(" ")
		}
		out.WriteString(line)
	}
	return out.String()
}
