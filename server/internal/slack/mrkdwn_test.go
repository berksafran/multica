package slack

import "testing"

func TestConvertMarkdownToSlack(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty stays empty", "", ""},
		{"plain text untouched", "hello world", "hello world"},

		{"bold double-asterisk", "**bold**", "*bold*"},
		{"bold double-underscore", "__bold__", "*bold*"},
		{"bold inside sentence", "say **hello** there", "say *hello* there"},
		{"two bold spans on one line", "**a** and **b**", "*a* and *b*"},

		{"strikethrough", "~~gone~~", "~gone~"},

		{"link", "[click](https://example.com)", "<https://example.com|click>"},
		{
			"link inside text",
			"see [PR #168](https://github.com/x/y/pull/168) for details",
			"see <https://github.com/x/y/pull/168|PR #168> for details",
		},

		{"h1 header", "# Title", "*Title*"},
		{"h2 header", "## Subtitle", "*Subtitle*"},
		{"h3 header with trailing space", "### Heading   ", "*Heading*"},

		{
			"mixed: header + bold + link",
			"# PRs\n**Repo X**\n- [#1](https://x/1) — opened",
			"*PRs*\n*Repo X*\n- <https://x/1|#1> — opened",
		},

		{
			"inline code preserved verbatim",
			"run `**not bold**` to inspect",
			"run `**not bold**` to inspect",
		},
		{
			"fenced code block preserved verbatim",
			"```\n**still raw** [link](x)\n```",
			"```\n**still raw** [link](x)\n```",
		},
		{
			"prose around fenced block still converts",
			"**before**\n```\n**raw**\n```\n**after**",
			"*before*\n```\n**raw**\n```\n*after*",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvertMarkdownToSlack(tt.in)
			if got != tt.want {
				t.Errorf("\n got:  %q\nwant:  %q", got, tt.want)
			}
		})
	}
}
