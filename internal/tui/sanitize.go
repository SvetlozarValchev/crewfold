package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const maxSanitizedRunes = 4096

func sanitizeLine(value string) string {
	return sanitize(value)
}

func sanitize(value string) string {
	value = strings.ToValidUTF8(value, "")
	var builder strings.Builder
	if len(value) > maxSanitizedRunes*4 {
		builder.Grow(maxSanitizedRunes * 4)
	} else {
		builder.Grow(len(value))
	}
	spacePending := false
	written := 0
	for _, current := range value {
		if written >= maxSanitizedRunes {
			break
		}
		if current == '\r' || current == '\n' || current == '\t' {
			spacePending = builder.Len() > 0
			continue
		}
		if isUnsafeControl(current) {
			continue
		}
		if spacePending {
			if written+1 >= maxSanitizedRunes {
				break
			}
			builder.WriteByte(' ')
			written++
			spacePending = false
		}
		builder.WriteRune(current)
		written++
	}
	return builder.String()
}

func isUnsafeControl(current rune) bool {
	if current <= 0x1f || current == 0x7f || current >= 0x80 && current <= 0x9f {
		return true
	}
	switch current {
	case 0x061c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2028, 0x2029, 0x2066, 0x2067, 0x2068, 0x2069, 0x206a, 0x206b, 0x206c, 0x206d, 0x206e, 0x206f:
		return true
	default:
		return false
	}
}

func fitLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = sanitizeLine(value)
	ellipsis := "…"
	limit := width - lipgloss.Width(ellipsis)
	if limit <= 0 {
		return strings.Repeat(" ", width)
	}
	maxRunes := width*4 + 32
	if maxRunes > 1024 {
		maxRunes = 1024
	}
	value, bounded := boundRunes(value, maxRunes)
	used := lipgloss.Width(value)
	truncated := bounded || used > width
	if !truncated {
		return value + strings.Repeat(" ", width-used)
	}
	prefix := lipgloss.NewStyle().MaxWidth(limit).Render(value)
	result := prefix + ellipsis
	return result + strings.Repeat(" ", width-lipgloss.Width(result))
}

func boundRunes(value string, limit int) (string, bool) {
	if limit <= 0 {
		return "", value != ""
	}
	var builder strings.Builder
	count := 0
	for _, current := range value {
		if count == limit {
			return builder.String(), true
		}
		builder.WriteRune(current)
		count++
	}
	return builder.String(), false
}
