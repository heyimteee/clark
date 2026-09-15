package gateway

import "strings"

// MessagePrefix brands every outbound message on WhatsApp, where the backticks
// render as monospace and a single newline flows into the body. IMessagePrefix
// is the plain-text equivalent for iMessage, which cannot render backticks, so
// it ends with a blank line to visually separate the brand from the body.
const (
	MessagePrefix  = "`🤵🏻‍♂️[CLARK]`\n"
	IMessagePrefix = "🤵🏻‍♂️[CLARK]\n\n"
)

// PrefixMessage prepends clark's WhatsApp branding to an outbound message.
func PrefixMessage(text string) string {
	return MessagePrefix + text
}

// PrefixIMessage prepends clark's plain-text branding to an iMessage.
func PrefixIMessage(text string) string {
	return IMessagePrefix + text
}

// SanitizeWhatsAppRichText drops lone * and _ markers so one-sided model
// output cannot corrupt WhatsApp formatting for the whole message. Code
// spans (```...``` and `...`) pass through verbatim; word-internal
// underscores (snake_case), symbol-adjacent markers (2*3), and line-leading
// list bullets never count as formatting. Remaining markers pair greedily
// left to right; a leftover last marker is removed. ~strikethrough~ is
// deliberately out of scope.
func SanitizeWhatsAppRichText(text string) string {
	type seg struct {
		s    string
		code bool
	}
	var segs []seg
	var cur strings.Builder
	flush := func(code bool) {
		if cur.Len() > 0 {
			segs = append(segs, seg{cur.String(), code})
			cur.Reset()
		}
	}
	rs := []rune(text)
	for i := 0; i < len(rs); {
		if rs[i] == '`' {
			fence := 1
			if i+2 < len(rs) && rs[i+1] == '`' && rs[i+2] == '`' {
				fence = 3
			} else if i+1 < len(rs) && rs[i+1] == '`' {
				// `` alone is not a span; treat literally below.
				cur.WriteRune(rs[i])
				i++
				continue
			}
			// Find the matching closer onward.
			end := -1
			for j := i + fence; j+fence <= len(rs); j++ {
				match := true
				for k := 0; k < fence; k++ {
					if rs[j+k] != '`' {
						match = false
						break
					}
				}
				if match {
					end = j
					break
				}
			}
			if end < 0 {
				// Unclosed fence: literal text, still scannable.
				cur.WriteRune(rs[i])
				i++
				continue
			}
			flush(false)
			segs = append(segs, seg{string(rs[i : end+fence]), true})
			i = end + fence
			continue
		}
		cur.WriteRune(rs[i])
		i++
	}
	flush(false)

	var out strings.Builder
	for _, sg := range segs {
		if sg.code {
			out.WriteString(sg.s)
			continue
		}
		out.WriteString(balanceMarkers(sg.s))
	}
	return out.String()
}

// balanceMarkers pairs * and _ formatting markers independently, dropping a
// leftover last marker of either kind.
func balanceMarkers(s string) string {
	for _, mark := range []rune{'*', '_'} {
		s = balanceRune(s, mark)
	}
	return s
}

func balanceRune(s string, mark rune) string {
	rs := []rune(s)
	if len(rs) <= 1 {
		return s // nothing to pair; never produce an empty send
	}
	var idx []int
	for i, r := range rs {
		if r != mark {
			continue
		}
		if isListBullet(rs, i) || !isFormatMarker(rs, i) {
			continue
		}
		idx = append(idx, i)
	}
	if len(idx)%2 == 0 {
		return s
	}
	drop := idx[len(idx)-1]
	return string(append(rs[:drop], rs[drop+1:]...))
}

// isListBullet reports a marker starting a bulleted line ("* item").
func isListBullet(rs []rune, i int) bool {
	if i != 0 && rs[i-1] != '\n' {
		return false
	}
	return i+1 < len(rs) && rs[i+1] == ' '
}

// isFormatMarker reports whether the marker at i can open or close
// formatting: it needs a non-space on at least one side, and must not sit
// inside a word on both sides (snake_case, arithmetic). A trailing marker
// with nothing after it can never format anything, so it counts (and, when
// unmatched, is dropped); a mid-string space-surrounded marker stays
// literal.
func isFormatMarker(rs []rune, i int) bool {
	var left, right rune
	hasLeft := i > 0
	hasRight := i+1 < len(rs)
	if hasLeft {
		left = rs[i-1]
	}
	if hasRight {
		right = rs[i+1]
	}
	if hasLeft && hasRight && isWordChar(left) && isWordChar(right) {
		return false
	}
	leftSpace := !hasLeft || isSpaceChar(left)
	rightSpace := !hasRight || isSpaceChar(right)
	if leftSpace && rightSpace {
		return !hasRight
	}
	return true
}

func isWordChar(r rune) bool {
	return r == '_' ||
		('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') ||
		('0' <= r && r <= '9') ||
		r > 127 // letters with diacritics, CJK, emoji-adjacent marks
}

func isSpaceChar(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}
