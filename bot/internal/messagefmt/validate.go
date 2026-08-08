package messagefmt

import (
	"fmt"
	"regexp"
	"strings"

	tele "gopkg.in/telebot.v3"
)

// markdownV2Constructs mirrors the protection patterns used by the renderer so
// validation and rendering agree on what counts as intentional markup. Order
// matters: longer/outer constructs must be consumed before inner ones, so that
// characters inside a link target are never mistaken for stray specials.
var markdownV2Constructs = []*regexp.Regexp{
	regexp.MustCompile("(?s)```[\\s\\S]*?```"),
	regexp.MustCompile("[`][^`\n]+[`]"),
	regexp.MustCompile(`!\[([^\]]*?)\]\(([^)]+)\)`),
	regexp.MustCompile(`\[([^\]]+?)\]\(([^)]+)\)`),
	regexp.MustCompile(`\|\|[^|]+\|\|`),
	regexp.MustCompile(`__[^_]+__`),
	regexp.MustCompile(`~[^~]+~`),
	regexp.MustCompile(`\*[^*]+\*`),
	regexp.MustCompile(`_[^_]+_`),
}

// consumedMarker replaces a recognised construct. It is not a MarkdownV2
// special character, so it never triggers a false positive, while still keeping
// neighbouring text separated (unlike an empty replacement, which could glue
// two unrelated fragments into a bogus construct).
const consumedMarker = "\x00"

func isMarkdownV2Special(ch rune) bool {
	for _, special := range markdownV2Specials {
		if string(ch) == special {
			return true
		}
	}
	return false
}

// IsMarkdownV2Mode reports whether the configured parse mode ends up being sent
// to Telegram as MarkdownV2. An empty parse mode resolves to MarkdownV2 too,
// which is exactly why templates without an explicit mode still have to be
// valid MarkdownV2.
func IsMarkdownV2Mode(mode string) bool {
	return ResolveParseMode(mode) == tele.ModeMarkdownV2
}

// ValidateMarkdownV2 reports whether text would be accepted by Telegram as a
// MarkdownV2 message body. It strips well-formed markup first and then requires
// every remaining special character to be backslash-escaped, which is the same
// rule Telegram enforces server-side.
//
// The check is intentionally conservative about what it treats as markup: a
// construct that does not match exactly (for example "[text] (url)" with a
// space before the parenthesis) is *not* consumed, so its brackets and
// parentheses are correctly reported as unescaped.
func ValidateMarkdownV2(text string) error {
	stripped := text
	for _, pattern := range markdownV2Constructs {
		stripped = pattern.ReplaceAllString(stripped, consumedMarker)
	}

	lines := strings.Split(stripped, "\n")
	for i, line := range lines {
		// A leading ">" is a blockquote marker rather than a literal character.
		if strings.HasPrefix(line, ">") {
			lines[i] = strings.TrimPrefix(line, ">")
		}
	}
	stripped = strings.Join(lines, "\n")

	runes := []rune(stripped)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '\\' {
			// Skip the escaped character; a trailing backslash escapes nothing
			// and simply ends the scan.
			i++
			continue
		}
		if isMarkdownV2Special(ch) {
			return fmt.Errorf("character %q must be escaped with a preceding backslash", string(ch))
		}
	}
	return nil
}

// ValidateTemplate validates a message template against the parse mode it will
// actually be sent with. Modes that do not resolve to MarkdownV2 are accepted
// as-is, because they are not affected by MarkdownV2 escaping rules.
func ValidateTemplate(text string, parseMode string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if !IsMarkdownV2Mode(parseMode) {
		return nil
	}
	return ValidateMarkdownV2(text)
}
