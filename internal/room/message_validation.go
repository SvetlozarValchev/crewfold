package room

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const MaximumInlineMessageRunes = 320

// ValidateSharedMessage protects the canonical room feed from substantial,
// unstructured transcript prose regardless of which transport submitted it.
func ValidateSharedMessage(body string) error {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	count := utf8.RuneCountInString(body)
	if count <= MaximumInlineMessageRunes || hasMarkdownBlock(body) {
		return nil
	}
	return fmt.Errorf("message is %d characters of unstructured prose; split it into short Markdown paragraphs or bullets, or upload a document", count)
}

func hasMarkdownBlock(body string) bool {
	if strings.Contains(body, "\n\n") {
		return true
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimLeft(line, " \t")
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") || strings.HasPrefix(line, "#### ") || strings.HasPrefix(line, "##### ") || strings.HasPrefix(line, "###### ") || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") || strings.HasPrefix(line, "> ") || strings.HasPrefix(line, "```") || (strings.HasPrefix(line, "|") && strings.Count(line, "|") >= 2) {
			return true
		}
		if dot := strings.Index(line, ". "); dot > 0 {
			numbered := true
			for _, character := range line[:dot] {
				if character < '0' || character > '9' {
					numbered = false
					break
				}
			}
			if numbered {
				return true
			}
		}
	}
	return false
}
