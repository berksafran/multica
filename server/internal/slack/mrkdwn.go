package slack

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Slack's chat.postMessage `text` field accepts only Slack's mrkdwn
// dialect, not standard Markdown — `**bold**`, `[text](url)`, and
// `# Headers` all render as literal characters instead of formatted
// text. LLM output uses standard Markdown, so we translate before
// posting. See https://api.slack.com/reference/surfaces/formatting.
//
// Translations applied (in order):
//   - fenced code blocks and inline code are stashed so their bodies
//     stay verbatim
//   - `**bold**` / `__bold__` -> `*bold*` (single asterisk = Slack bold)
//   - `[label](url)` -> `<url|label>`
//   - `~~strike~~` -> `~strike~`
//   - `# Header` / `## Header` / ... -> `*Header*`
//
// Standard-Markdown italic `*foo*` is intentionally NOT remapped to
// Slack `_foo_` italic: after the bold pass single asterisks become
// ambiguous (some are now Slack bold), and disambiguating them
// reliably needs a parser. Leaving them as-is means a stray italic
// renders as plain text in Slack — acceptable tradeoff vs. corrupting
// bold spans.
func ConvertMarkdownToSlack(text string) string {
	if text == "" {
		return text
	}

	// Slack mrkdwn has no table primitive — `|` / `---` render as
	// literal characters. Re-emit GFM tables as monospaced code blocks
	// so columns stay aligned. Cell content becomes literal (any inner
	// markdown is lost), which is the deliberate tradeoff vs. a bullet
	// list that would lose the column structure entirely.
	text = convertTables(text)

	// Stash fenced code blocks (```...```) and inline code (`...`)
	// behind sentinels so the regexes below never touch their bodies.
	// Sentinels use \x01 which never appears in normal text.
	stash := newStash()
	text = codeBlockRe.ReplaceAllStringFunc(text, stash.put)
	text = inlineCodeRe.ReplaceAllStringFunc(text, stash.put)

	// Bold: **x** and __x__ -> *x* (Slack bold).
	text = boldStarRe.ReplaceAllString(text, "*$1*")
	text = boldUnderscoreRe.ReplaceAllString(text, "*$1*")

	// Strikethrough: ~~x~~ -> ~x~ (Slack strike).
	text = strikeRe.ReplaceAllString(text, "~$1~")

	// Links: [label](url) -> <url|label>.
	text = linkRe.ReplaceAllString(text, "<$2|$1>")

	// Headers: leading #...# -> *Header*. We drop the hash run so the
	// output reads naturally; Slack has no native heading concept.
	text = headerRe.ReplaceAllString(text, "*$1*")

	// Unstash code spans verbatim.
	text = stash.restore(text)
	return text
}

var (
	codeBlockRe      = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRe     = regexp.MustCompile("`[^`\n]+`")
	boldStarRe       = regexp.MustCompile(`\*\*([^\n*]+?)\*\*`)
	boldUnderscoreRe = regexp.MustCompile(`__([^\n_]+?)__`)
	strikeRe         = regexp.MustCompile(`~~([^\n~]+?)~~`)
	linkRe           = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	headerRe         = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)
)

// codeStash holds verbatim code spans we extracted before running the
// formatting rewrites. Sentinels are `\x01MRK_<index>\x01` so the
// markdown regexes can't accidentally match them.
type codeStash struct {
	items []string
}

func newStash() *codeStash { return &codeStash{} }

func (s *codeStash) put(raw string) string {
	idx := len(s.items)
	s.items = append(s.items, raw)
	return "\x01MRK_" + strconv.Itoa(idx) + "\x01"
}

func (s *codeStash) restore(text string) string {
	if len(s.items) == 0 {
		return text
	}
	for i, raw := range s.items {
		text = strings.Replace(text, "\x01MRK_"+strconv.Itoa(i)+"\x01", raw, 1)
	}
	return text
}

// convertTables scans for GitHub-flavored Markdown tables (a header
// row followed by a separator line of dashes, then any number of body
// rows) and rewrites each as a fenced code block with columns padded
// to a common width. Non-table content passes through unchanged.
func convertTables(text string) string {
	if !strings.Contains(text, "|") {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		if i+1 < len(lines) && containsPipe(lines[i]) && tableSeparatorRe.MatchString(lines[i+1]) {
			header := splitTableRow(lines[i])
			j := i + 2
			var body [][]string
			for j < len(lines) && containsPipe(lines[j]) {
				row := splitTableRow(lines[j])
				for len(row) < len(header) {
					row = append(row, "")
				}
				if len(row) > len(header) {
					row = row[:len(header)]
				}
				body = append(body, row)
				j++
			}
			out = append(out, renderTableAsCodeBlock(header, body))
			i = j
			continue
		}
		out = append(out, lines[i])
		i++
	}
	return strings.Join(out, "\n")
}

func containsPipe(line string) bool {
	return strings.Contains(line, "|") && strings.TrimSpace(line) != ""
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

func renderTableAsCodeBlock(header []string, body [][]string) string {
	cols := len(header)
	widths := make([]int, cols)
	for i, h := range header {
		if w := utf8.RuneCountInString(h); w > widths[i] {
			widths[i] = w
		}
	}
	for _, row := range body {
		for i, cell := range row {
			if w := utf8.RuneCountInString(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	for i, w := range widths {
		if w < 1 {
			widths[i] = 1
		}
	}

	const gap = "  "
	var b strings.Builder
	b.WriteString("```\n")
	writeRow := func(cells []string) {
		for i, c := range cells {
			if i > 0 {
				b.WriteString(gap)
			}
			if i == len(cells)-1 {
				b.WriteString(c)
			} else {
				b.WriteString(padRight(c, widths[i]))
			}
		}
		b.WriteByte('\n')
	}
	writeRow(header)
	sep := make([]string, cols)
	for i := range sep {
		sep[i] = strings.Repeat("-", widths[i])
	}
	writeRow(sep)
	for _, row := range body {
		writeRow(row)
	}
	b.WriteString("```")
	return b.String()
}

func padRight(s string, width int) string {
	pad := width - utf8.RuneCountInString(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// Matches GFM table separator lines: at least two columns of three or
// more dashes, with optional alignment colons and surrounding pipes.
var tableSeparatorRe = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$`)
