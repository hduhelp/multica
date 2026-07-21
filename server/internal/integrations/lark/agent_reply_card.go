package lark

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Agent reply card builder. The card layout — a status-coloured header over a
// markdown body — is adapted from happyclaw's Feishu v2 card design (MIT,
// riba2534/happyclaw), reimplemented for Multica's RenderInput and Lark's
// schema-2.0 card envelope (the same envelope SendMarkdownCard posts).
//
// It backs the failure card on the EventTaskFailed path: a red-header card with
// the error in the body, visually distinct from a normal reply. A SUCCESSFUL
// reply does not use this builder — sendChatReply posts plain text or a
// headerless markdown card so an ordinary answer reads like a normal IM
// message, not a titled status card.

// cardHeaderTemplate maps a card kind to a Lark header template colour. Colour
// is the primary status signal so the chrome stays language-neutral.
func cardHeaderTemplate(kind CardKind) string {
	switch kind {
	case CardKindError:
		return "red"
	default:
		return "blue"
	}
}

// cardHeader returns the card's status header, or nil for a headerless card. A
// successful terminal reply (CardKindFinal) drops the header entirely: the
// agent identity already shows as the Feishu message sender, so a repeated
// title is pure chrome that only shrinks the reply and makes an ordinary answer
// look like a system notification. An error keeps a red header because the
// failure distinction is genuinely useful at a glance.
//
// The header carries ONLY a plain_text title. It never puts the reply body into
// a subtitle/description line — doing so makes Lark collapse the answer into a
// truncated, ellipsized header (the happyclaw #488 pitfall).
func cardHeader(in RenderInput) map[string]any {
	if in.Kind == CardKindFinal {
		return nil
	}
	title := in.AgentName
	if title == "" {
		title = "Multica"
	}
	return map[string]any{
		"template": cardHeaderTemplate(in.Kind),
		"title":    map[string]any{"tag": "plain_text", "content": title},
	}
}

// cardBody derives the main markdown body for a kind.
func cardBody(in RenderInput) string {
	switch in.Kind {
	case CardKindThinking:
		return "…"
	case CardKindRunning:
		if strings.TrimSpace(in.Content) != "" {
			return in.Content
		}
		return "…"
	case CardKindError:
		if in.ErrorMessage != "" {
			return "**Run failed:** " + in.ErrorMessage
		}
		return "**Run failed.**"
	default: // CardKindFinal
		return in.Content
	}
}

// cardSummary is the notification/list-preview text Lark shows for the card. A
// short slice of the body reads better in a notification than a generic title.
func cardSummary(agentName, body string) string {
	preview := strings.TrimSpace(stripMarkdownForPreview(body))
	if preview == "" {
		preview = agentName
	}
	if len([]rune(preview)) > 80 {
		preview = string([]rune(preview)[:80]) + "…"
	}
	return preview
}

// buildAgentReplyCard renders a schema-2.0 interactive card JSON string: a
// status header (dropped for a successful final reply) over a table-downgraded
// markdown body, plus a summary for the notification preview.
func buildAgentReplyCard(in RenderInput) (CardRender, error) {
	switch in.Kind {
	case CardKindThinking, CardKindRunning, CardKindFinal, CardKindError:
	default:
		return CardRender{}, fmt.Errorf("unknown card kind %q", in.Kind)
	}

	title := in.AgentName
	if title == "" {
		title = "Multica"
	}
	body := cardBody(in)

	config := map[string]any{
		"update_multi":   true,
		"enable_forward": true,
		"width_mode":     "fill",
		"summary":        map[string]any{"content": cardSummary(title, body)},
	}

	doc := map[string]any{
		"schema": "2.0",
		"config": config,
		"body": map[string]any{
			"direction":        "vertical",
			"vertical_spacing": "medium",
			"elements": []any{
				map[string]any{
					"tag":     "markdown",
					"content": downgradeMarkdownTablesForLark(body),
				},
			},
		},
	}
	if header := cardHeader(in); header != nil {
		doc["header"] = header
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return CardRender{}, err
	}
	return CardRender{JSON: string(raw)}, nil
}

// stripMarkdownForPreview flattens common markdown markers so the summary is a
// clean prose preview rather than raw syntax. It is intentionally shallow — a
// notification preview does not need a full markdown parser.
func stripMarkdownForPreview(s string) string {
	replacer := strings.NewReplacer(
		"**", "", "__", "", "`", "", "#", "", ">", "", "*", "", "~~", "",
	)
	out := replacer.Replace(s)
	// Collapse whitespace/newlines into single spaces for a one-line preview.
	return strings.Join(strings.Fields(out), " ")
}
