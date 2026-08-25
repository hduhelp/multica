package lark

import (
	"strings"
	"testing"
)

// splitSourceHeader separates the <source> stamp from the body it precedes.
// Returns an empty header when the body was not stamped.
func splitSourceHeader(body string) (header, rest string) {
	const close = "</source>\n\n"
	if !strings.HasPrefix(body, "<source ") {
		return "", body
	}
	i := strings.Index(body, close)
	if i < 0 {
		return body, ""
	}
	return body[:i+len("</source>")], body[i+len(close):]
}

// A handle is inert unless the agent knows which platform resolves it: an id
// like img_x names nothing on its own. But the stamped body is persisted and
// replayed as history, so it must not ride along on messages that reference
// nothing.
func TestSourceStampFollowsReferencedContent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		in    InboundMessage
		stamp bool
	}{
		{
			name:  "plain text references nothing",
			in:    InboundMessage{MessageType: "text", Body: "线上还能用吗", ChatID: "oc_a", ChatType: ChatTypeP2P, MessageID: "om_1"},
			stamp: false,
		},
		{
			name:  "image is referenced, not inlined",
			in:    InboundMessage{MessageType: "image", Body: "[Image message_id=om_2 image_key=img_x]", ChatID: "oc_a", ChatType: ChatTypeP2P, MessageID: "om_2"},
			stamp: true,
		},
		{
			name:  "file is referenced",
			in:    InboundMessage{MessageType: "file", Body: `[File message_id=om_3 file_key=f_x name="log.txt"]`, ChatID: "oc_a", ChatType: ChatTypeP2P, MessageID: "om_3"},
			stamp: true,
		},
		{
			name: "a quoted id is not a reference — the text is already inlined",
			in: InboundMessage{MessageType: "text", ChatID: "oc_a", ChatType: ChatTypeP2P, MessageID: "om_4",
				Body: "<quoted_message message_id=\"om_p\" sender=\"User 1\">\nhello\n</quoted_message>\n\n看这个"},
			stamp: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &inboundEnricher{}
			got := e.stampSource(tc.in)
			header, rest := splitSourceHeader(got.Body)
			if tc.stamp && header == "" {
				t.Fatalf("want stamp, got %q", got.Body)
			}
			if !tc.stamp {
				if header != "" {
					t.Fatalf("unexpected stamp on %q", got.Body)
				}
				return
			}
			for _, want := range []string{`channel="feishu"`, `chat_id="oc_a"`, `message_id="` + tc.in.MessageID + `"`} {
				if !strings.Contains(header, want) {
					t.Errorf("header missing %s: %q", want, header)
				}
			}
			if rest != tc.in.Body {
				t.Errorf("stamp must not alter the body: got %q want %q", rest, tc.in.Body)
			}
		})
	}
}
