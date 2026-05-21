package slack

import (
	"regexp"
	"strconv"
	"strings"
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
