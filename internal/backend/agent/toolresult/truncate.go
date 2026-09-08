package toolresult

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const bytesNoticeNeedle = " result exceeded "

// CutPrefix 按字节上限截取前缀并保证 UTF-8 完整。
func CutPrefix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	if limit > len(text) {
		limit = len(text)
	}
	truncated := text[:limit]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

// CutSuffix 按字节上限截取后缀并保证 UTF-8 完整。
func CutSuffix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	if start < 0 {
		start = 0
	}
	suffix := text[start:]
	for !utf8.ValidString(suffix) && start < len(text) {
		start++
		suffix = text[start:]
	}
	return suffix
}

// CutBytes 截取字节切片；未截断时返回原切片。
func CutBytes(value []byte, limit int) ([]byte, bool) {
	if limit <= 0 || len(value) <= limit {
		return value, false
	}
	return append([]byte(nil), value[:limit]...), true
}

// BytesNotice 构造计入预算的字节截断提示。
func BytesNotice(label string, limit int, kept int, original int) string {
	return fmt.Sprintf("[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", label, limit, kept, original)
}

// CountNotice 构造计入预算的数量截断提示，unit 为 items/resources 等。
func CountNotice(label string, unit string, limit int, kept int, original int) string {
	return fmt.Sprintf("[truncated: %s result exceeded %d %s; showing %d of %d %s]", label, limit, unit, kept, original, unit)
}

// ItemsNotice 构造 MCP content items 使用的无 result 词提示。
func ItemsNotice(label string, limit int, kept int, original int) string {
	return fmt.Sprintf("[truncated: %s exceeded %d items; showing %d of %d items]", label, limit, kept, original)
}

// ContainsBytesNotice 判断文本是否已含指定 label 的字节截断提示。
func ContainsBytesNotice(text string, label string) bool {
	return strings.Contains(text, "[truncated: "+label+bytesNoticeNeedle)
}

// TruncateTail 从尾部截断并在预算内附加字节提示。limit<=0 或未超限时原样返回。
func TruncateTail(label string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := "\n\n" + BytesNotice(label, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return CutPrefix(text, limit)
		}
		kept := CutPrefix(text, keep)
		nextNotice := "\n\n" + BytesNotice(label, limit, len(kept), original)
		output := strings.TrimRight(kept, "\n") + nextNotice
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

// TruncateMiddle 省略中段并在预算内附加提示。limit<=0 或未超限时原样返回。
func TruncateMiddle(label string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", label, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return CutPrefix(text, limit)
		}
		headLimit := keep / 2
		tailLimit := keep - headLimit
		head := CutPrefix(text, headLimit)
		tail := CutSuffix(text, tailLimit)
		kept := len(head) + len(tail)
		nextNotice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", label, limit, kept, original)
		output := head + nextNotice + tail
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

// TruncateLine 截断单行并在预算内附加行级提示。
func TruncateLine(label string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf(" [truncated: %s line exceeded %d bytes; showing %d of %d bytes]", label, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return CutPrefix(text, limit)
		}
		kept := CutPrefix(text, keep)
		nextNotice := fmt.Sprintf(" [truncated: %s line exceeded %d bytes; showing %d of %d bytes]", label, limit, len(kept), original)
		output := kept + nextNotice
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

// TruncateLines 按行应用 TruncateLine。
func TruncateLines(label string, text string, lineLimit int) string {
	if lineLimit <= 0 || text == "" {
		return text
	}
	parts := strings.SplitAfter(text, "\n")
	for index, part := range parts {
		newline := ""
		body := part
		if strings.HasSuffix(part, "\n") {
			body = strings.TrimSuffix(part, "\n")
			newline = "\n"
		}
		parts[index] = TruncateLine(label, body, lineLimit) + newline
	}
	return strings.Join(parts, "")
}
