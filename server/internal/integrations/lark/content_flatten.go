package lark

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// flattenContent renders a Lark message's body.content — the raw,
// JSON-encoded string Lark double-encodes — into plain text, dispatching
// on msg_type. It is the shared structural step used by BOTH ingress
// paths:
//
//   - the inbound decoder, for the user's own text / post message, and
//   - the enricher, for the quoted-reply parent and merge_forward child
//     messages it pulls back over the IM REST API.
//
// Mention placeholders (@_user_N) are preserved verbatim; the caller is
// responsible for resolving them against the message's mentions[] array
// via resolveMentions. The two ingress shapes (WS receive event vs IM
// REST item) carry the mentions array differently — only the caller
// knows which one applies — so flattening stays mention-agnostic.
//
// Non-text media types render as a stable bracketed placeholder so the
// agent sees that *something* was attached without this fast path
// downloading the binary; the detached media resolver separately fetches
// the resource and binds it as a chat attachment, with the placeholder
// as the durable fallback. merge_forward is intercepted by the enricher
// before it reaches here (expanding it needs an HTTP round-trip); the
// inline placeholder is only a fallback for a forward nested inside
// another forward.
func flattenContent(msgType, rawContent, messageID string) string {
	switch msgType {
	case "text":
		return extractTextBody(rawContent)
	case "post":
		return flattenPostContent(rawContent)
	case "image":
		return mediaPlaceholder("Image", messageID, rawContent)
	case "file":
		return mediaPlaceholder("File", messageID, rawContent)
	case "audio":
		return mediaPlaceholder("Audio", messageID, rawContent)
	case "media", "video":
		return mediaPlaceholder("Video", messageID, rawContent)
	case "sticker":
		return "[Sticker]"
	case "interactive":
		return flattenInteractiveCard(rawContent)
	case "share_chat":
		return refPlaceholder("Shared Chat", "chat_id", rawContent)
	case "share_user":
		return refPlaceholder("Shared User Card", "user_id", rawContent)
	case "system":
		return "[System Message]"
	case "merge_forward":
		return forwardPlaceholder(messageID)
	default:
		return ""
	}
}

// larkMediaContent is the subset of a media message's body.content that
// identifies the binary. Lark's download endpoint keys on the OWNING message id
// plus the resource key, which is why the placeholders below carry both.
type larkMediaContent struct {
	ImageKey string `json:"image_key"`
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
	Name     string `json:"name"`
}

// mediaPlaceholder renders a non-text attachment as a bracketed marker that
// carries its fetch handle. The flatten path deliberately does not download the
// binary — that is the media resolver's job for the trigger message — but a
// placeholder with no identifiers strands everything else: an image someone
// posted earlier in the group becomes "[Image]" and the agent cannot act on it
// even though it has a Lark CLI. With message_id and the resource key it can.
func mediaPlaceholder(label, messageID, rawContent string) string {
	var c larkMediaContent
	if rawContent != "" {
		_ = json.Unmarshal([]byte(rawContent), &c)
	}
	parts := make([]string, 0, 4)
	if messageID != "" {
		parts = append(parts, "message_id="+messageID)
	}
	if c.ImageKey != "" {
		parts = append(parts, "image_key="+c.ImageKey)
	}
	if c.FileKey != "" {
		parts = append(parts, "file_key="+c.FileKey)
	}
	if name := firstNonEmptyStr(c.FileName, c.Name); name != "" {
		parts = append(parts, "name="+strconv.Quote(name))
	}
	if len(parts) == 0 {
		return "[" + label + "]"
	}
	return "[" + label + " " + strings.Join(parts, " ") + "]"
}

// forwardPlaceholder marks a merged forward. Its own body.content is a fixed
// sentinel, and the bundled messages only come back as the items[] of a
// GetMessage call — so the message id is the entire handle, and without it the
// bundle is unreachable.
func forwardPlaceholder(messageID string) string {
	if messageID == "" {
		return "[forwarded messages]"
	}
	return "[forwarded messages message_id=" + messageID + "]"
}

// refPlaceholder marks a shared chat/user card, carrying the id it points at.
func refPlaceholder(label, field, rawContent string) string {
	var m map[string]any
	if rawContent != "" && json.Unmarshal([]byte(rawContent), &m) == nil {
		if v, ok := m[field].(string); ok && v != "" {
			return "[" + label + " " + field + "=" + v + "]"
		}
	}
	return "[" + label + "]"
}

// maxCardTextRunes caps how much of an interactive card is inlined. A card can
// be arbitrarily long (an agent's own multi-table analysis is one), and the
// recent-context window carries several messages, so an uncapped card can crowd
// out the rest of the prompt.
const maxCardTextRunes = 600

// flattenInteractiveCard recovers the readable text of an interactive card.
// Cards are the shape an agent's own previous answers come back in, so
// collapsing them to "[interactive card]" made every turn lose the last one's
// conclusions. The schema is open-ended and version-dependent, so rather than
// model it, walk the JSON and keep the values of the keys that carry display
// text, in document order.
func flattenInteractiveCard(rawContent string) string {
	if rawContent == "" {
		return "[interactive card]"
	}
	var root any
	if err := json.Unmarshal([]byte(rawContent), &root); err != nil {
		return "[interactive card]"
	}
	var out []string
	collectCardText(root, &out)
	if len(out) == 0 {
		return "[interactive card]"
	}
	text := strings.Join(out, "\n")
	if runes := []rune(text); len(runes) > maxCardTextRunes {
		text = string(runes[:maxCardTextRunes]) + " …[card truncated]"
	}
	return "[interactive card]\n" + text
}

// cardTextKeys are the keys whose string values are display text in every card
// schema we have seen (1.0 elements, 2.0 body/elements, and the header title).
var cardTextKeys = map[string]bool{"content": true, "text": true, "title": true, "subtitle": true}

// cardKeyOrder ranks the structural keys of a card by where they render, so a
// flattened card reads header-then-body regardless of decode order.
var cardKeyOrder = map[string]int{
	"header": 0, "title": 1, "subtitle": 2,
	"text": 3, "content": 4, "body": 5, "elements": 6,
}

func cardKeyRank(k string) int {
	if r, ok := cardKeyOrder[k]; ok {
		return r
	}
	return len(cardKeyOrder)
}

func collectCardText(node any, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		// A decoded object has no key order, and plain map iteration would
		// reorder a card's lines between identical inputs — prompt churn on a
		// message that did not change. Sorting alphabetically is deterministic
		// but not neutral: it puts "body" ahead of "header", printing a card's
		// title after its own text. Rank the structural keys in the order a
		// card reads instead, and fall back to alphabetical for the rest.
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.SliceStable(keys, func(i, j int) bool {
			ri, rj := cardKeyRank(keys[i]), cardKeyRank(keys[j])
			if ri != rj {
				return ri < rj
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			if s, ok := v[k].(string); ok {
				if cardTextKeys[k] {
					if t := strings.TrimSpace(s); t != "" {
						*out = append(*out, t)
					}
				}
				continue
			}
			collectCardText(v[k], out)
		}
	case []any:
		for _, item := range v {
			collectCardText(item, out)
		}
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// larkPostContent mirrors the RECEIVE-side shape of a `post` rich-text
// body.content. Crucially this is NOT the locale-wrapped form the SEND
// API takes ({"zh_cn": {...}}): an inbound post body.content unmarshals
// directly into {title, content}. content is a 2-D array — the outer
// array is the ordered list of paragraphs, each inner array the ordered
// spans of that paragraph; the newline between paragraphs is implicit in
// the array boundary, not a span.
type larkPostContent struct {
	Title   string           `json:"title"`
	Content [][]larkPostSpan `json:"content"`
}

// larkPostSpan is one node inside a post paragraph. Only the fields that
// carry renderable text are modelled; the tag set is extensible, so the
// flattener emits `text` for any unrecognized tag and skips it otherwise
// rather than failing.
type larkPostSpan struct {
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Href     string `json:"href"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	ImageKey string `json:"image_key"`
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
}

// flattenPostContent flattens a received `post` body.content into plain
// text: the title (when present) on its own first line, then one line
// per paragraph. Within a paragraph spans are joined with a single space
// — this matches Lark's own rendering, where logically separate chunks
// ("Lark 集成", then a link "PR #3277") read as space-separated words.
//
// A link span renders as "text (href)" so the URL survives into the
// agent's context; an `at` span renders as its @_user_N placeholder so
// a downstream resolveMentions pass can substitute the display name
// (falling back to the inline user_name when the placeholder is absent).
// Media spans degrade to the same bracketed placeholders flattenContent
// uses.
func flattenPostContent(raw string) string {
	if raw == "" {
		return ""
	}
	var doc larkPostContent
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return ""
	}

	var b strings.Builder
	write := func(line string) {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	if doc.Title != "" {
		write(doc.Title)
	}
	for _, para := range doc.Content {
		write(flattenPostParagraph(para))
	}
	return strings.TrimRight(b.String(), "\n")
}

func flattenPostParagraph(spans []larkPostSpan) string {
	parts := make([]string, 0, len(spans))
	for _, s := range spans {
		switch s.Tag {
		case "text", "code_block":
			if s.Text != "" {
				parts = append(parts, s.Text)
			}
		case "a":
			switch {
			case s.Text != "" && s.Href != "":
				parts = append(parts, s.Text+" ("+s.Href+")")
			case s.Text != "":
				parts = append(parts, s.Text)
			case s.Href != "":
				parts = append(parts, s.Href)
			}
		case "at":
			// Prefer the @_user_N placeholder so a later
			// resolveMentions pass can map it to a display name and
			// strip the bot's own mention; fall back to the inline
			// user_name when the placeholder is absent.
			switch {
			case s.UserID != "":
				parts = append(parts, s.UserID)
			case s.UserName != "":
				parts = append(parts, "@"+s.UserName)
			}
		case "img":
			parts = append(parts, mediaSpanPlaceholder("Image", s.ImageKey, "", ""))
		case "media":
			parts = append(parts, mediaSpanPlaceholder("Video", s.ImageKey, s.FileKey, s.FileName))
		case "emotion":
			// emoji_type is an enum key (e.g. "SMILE"), not display
			// text — skip it rather than leak the key.
		case "hr":
			parts = append(parts, "---")
		default:
			if s.Text != "" {
				parts = append(parts, s.Text)
			}
		}
	}
	return strings.Join(parts, " ")
}

// mediaSpanPlaceholder is mediaPlaceholder for a span inside a post: the keys
// are already parsed into the span, and the owning message id is added by the
// caller's own placeholder when it has one.
func mediaSpanPlaceholder(label, imageKey, fileKey, name string) string {
	parts := make([]string, 0, 3)
	if imageKey != "" {
		parts = append(parts, "image_key="+imageKey)
	}
	if fileKey != "" {
		parts = append(parts, "file_key="+fileKey)
	}
	if name != "" {
		parts = append(parts, "name="+strconv.Quote(name))
	}
	if len(parts) == 0 {
		return "[" + label + "]"
	}
	return "[" + label + " " + strings.Join(parts, " ") + "]"
}
