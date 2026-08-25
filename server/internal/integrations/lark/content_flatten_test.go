package lark

import (
	"strings"
	"testing"
)

// TestFlattenPostContent_IssueExample pins the exact rich-text `post`
// example from MUL-2951: a title line, a prose paragraph, and a
// paragraph mixing a text span with a hyperlink span. The link must
// render as "text (href)" so the URL survives into the agent's context.
func TestFlattenPostContent_IssueExample(t *testing.T) {
	t.Parallel()
	// Received-side post body.content (NOT locale-wrapped).
	raw := `{
		"title": "周报",
		"content": [
			[{ "tag": "text", "text": "本周完成：" }],
			[
				{ "tag": "text", "text": "Lark 集成" },
				{ "tag": "a", "href": "https://github.com/multica-ai/multica/pull/3277", "text": "PR #3277" }
			]
		]
	}`
	want := "周报\n本周完成：\nLark 集成 PR #3277 (https://github.com/multica-ai/multica/pull/3277)"
	if got := flattenPostContent(raw); got != want {
		t.Errorf("flattenPostContent()\n got = %q\nwant = %q", got, want)
	}
}

func TestFlattenPostContent_NoTitle(t *testing.T) {
	t.Parallel()
	raw := `{"content":[[{"tag":"text","text":"line one"}],[{"tag":"text","text":"line two"}]]}`
	want := "line one\nline two"
	if got := flattenPostContent(raw); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFlattenPostContent_MediaAndMentionSpans(t *testing.T) {
	t.Parallel()
	// at span carries the @_user_N placeholder (resolved later by
	// resolveMentions); media tags degrade to bracket placeholders;
	// emotion is skipped entirely.
	raw := `{"content":[[
		{"tag":"at","user_id":"@_user_1","user_name":""},
		{"tag":"text","text":"look"},
		{"tag":"img","image_key":"img_x"},
		{"tag":"emotion","emoji_type":"SMILE"}
	]]}`
	want := "@_user_1 look [Image image_key=img_x]"
	if got := flattenPostContent(raw); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFlattenPostContent_AtPrefersPlaceholderWhenBothPresent(t *testing.T) {
	t.Parallel()
	raw := `{"content":[[{"tag":"at","user_id":"@_user_1","user_name":"ReviewBot"}]]}`
	want := "@_user_1"
	if got := flattenPostContent(raw); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFlattenPostContent_AtFallsBackToUserNameWhenNoPlaceholder(t *testing.T) {
	t.Parallel()
	raw := `{"content":[[{"tag":"at","user_id":"","user_name":"ReviewBot"}]]}`
	want := "@ReviewBot"
	if got := flattenPostContent(raw); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFlattenPostContent_TopicGroupMentionSlashCommand(t *testing.T) {
	t.Parallel()
	raw := `{"content":[[{"tag":"at","user_id":"@_user_1","user_name":"ReviewBot"},{"tag":"text","text":" /issue review this"}]]}`
	flat := flattenPostContent(raw)
	m := larkMention{Key: "@_user_1", Name: "ReviewBot"}
	m.ID.OpenID = "ou_bot"
	mentions := []larkMention{m}
	got := resolveMentions(flat, mentions, "ou_bot", "")
	got = strings.TrimSpace(got)
	if want := "/issue review this"; got != want {
		t.Errorf("after resolveMentions got %q want %q (flat was %q)", got, want, flat)
	}
	if len(got) == 0 || got[0] != '/' {
		t.Errorf("first line should start with '/', got %q", got)
	}
}

func TestFlattenPostContent_Malformed(t *testing.T) {
	t.Parallel()
	if got := flattenPostContent("not json"); got != "" {
		t.Errorf("malformed content should flatten to empty, got %q", got)
	}
	if got := flattenPostContent(""); got != "" {
		t.Errorf("empty content should flatten to empty, got %q", got)
	}
}

func TestFlattenContent_DispatchByType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		msgType string
		content string
		want    string
	}{
		{"text", "text", `{"text":"hello"}`, "hello"},
		// Every non-text placeholder carries what the Lark download endpoint
		// needs — the owning message id plus the resource key — so an agent
		// with a Lark CLI can fetch it. A bare "[Image]" stranded it.
		{"image", "image", `{"image_key":"img_x"}`, "[Image message_id=om_1 image_key=img_x]"},
		{"file", "file", `{"file_key":"f","file_name":"a b.pdf"}`, `[File message_id=om_1 file_key=f name="a b.pdf"]`},
		{"audio", "audio", `{"file_key":"f"}`, "[Audio message_id=om_1 file_key=f]"},
		{"media", "media", `{"file_key":"f"}`, "[Video message_id=om_1 file_key=f]"},
		{"video", "video", `{"file_key":"f"}`, "[Video message_id=om_1 file_key=f]"},
		{"sticker", "sticker", `{"file_key":"f"}`, "[Sticker]"},
		{"interactive", "interactive", `{"title":"t"}`, "[interactive card]\nt"},
		{"share_chat", "share_chat", `{"chat_id":"oc"}`, "[Shared Chat chat_id=oc]"},
		// The bundle only comes back from GetMessage(message_id), so the id is
		// the whole handle.
		{"merge_forward", "merge_forward", `{"content":"Merged and Forwarded Message"}`, "[forwarded messages message_id=om_1]"},
		{"unknown", "totally_new_type", `{}`, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := flattenContent(tc.msgType, tc.content, "om_1"); got != tc.want {
				t.Errorf("flattenContent(%q) = %q want %q", tc.msgType, got, tc.want)
			}
		})
	}
}
