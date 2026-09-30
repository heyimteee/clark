package assistant

import (
	"context"
	"testing"

	"github.com/heyimteee/clark/internal/tools"
)

const dualJID = "6281267858909@s.whatsapp.net"

// TestHistoryKeyNamespacesIMessage is the core of #214: a person reachable on
// both apps keeps one VIP identity but a separate transcript per channel, so
// Clark can tell which app a remembered message came from.
func TestHistoryKeyNamespacesIMessage(t *testing.T) {
	imsgCtx := tools.WithTransport(context.Background(), iMessageTransport)
	waCtx := tools.WithTransport(context.Background(), "whatsapp")

	imKey := historyKey(imsgCtx, dualJID)
	waKey := historyKey(waCtx, dualJID)

	if imKey == waKey {
		t.Fatalf("both channels resolved to %q; their histories would merge", imKey)
	}
	if imKey != "imessage:"+dualJID {
		t.Errorf("iMessage key = %q, want the prefixed form", imKey)
	}
	// WhatsApp keeps the bare key, so existing history stays readable where it
	// is with no migration.
	if waKey != dualJID {
		t.Errorf("WhatsApp key = %q, want the unprefixed form for backwards compatibility", waKey)
	}
}

// TestHistoryKeyUnsetTransportStaysBare proves a caller that does not tag the
// transport keeps writing to the unprefixed key rather than inventing a scope.
func TestHistoryKeyUnsetTransportStaysBare(t *testing.T) {
	if got := historyKey(context.Background(), dualJID); got != dualJID {
		t.Errorf("key = %q, want the bare JID when no transport is tagged", got)
	}
}

// TestTransportTagIsCaseInsensitive proves "IMESSAGE" and "imessage" name the
// same channel, since the gateway passes its component name as written.
func TestTransportTagIsCaseInsensitive(t *testing.T) {
	ctx := tools.WithTransport(context.Background(), "  IMESSAGE  ")
	if got := historyKey(ctx, dualJID); got != "imessage:"+dualJID {
		t.Errorf("key = %q, want the namespaced form", got)
	}
}

// TestDualChannelHistoryStaysSeparate is the end-to-end property: the same VIP
// writes on both channels, and neither transcript leaks into the other.
func TestDualChannelHistoryStaysSeparate(t *testing.T) {
	// No VIP entry is needed: Record writes by key, and the point here is the
	// transcript split, not membership.
	svc, st, _ := newService(t)

	waCtx := tools.WithSender(tools.WithTransport(context.Background(), "whatsapp"), dualJID)
	imCtx := tools.WithSender(tools.WithTransport(context.Background(), iMessageTransport), dualJID)

	if err := svc.Record(waCtx, dualJID, "spoken on whatsapp"); err != nil {
		t.Fatalf("Record whatsapp: %v", err)
	}
	if err := svc.Record(imCtx, dualJID, "typed on imessage"); err != nil {
		t.Fatalf("Record imessage: %v", err)
	}

	waMsgs, err := st.RecentMessages(dualJID, 10)
	if err != nil {
		t.Fatalf("whatsapp history: %v", err)
	}
	if len(waMsgs) != 1 || waMsgs[0].Content != "spoken on whatsapp" {
		t.Errorf("whatsapp history = %+v, want only the WhatsApp turn", waMsgs)
	}

	imMsgs, err := st.RecentMessages("imessage:"+dualJID, 10)
	if err != nil {
		t.Fatalf("imessage history: %v", err)
	}
	if len(imMsgs) != 1 || imMsgs[0].Content != "typed on imessage" {
		t.Errorf("imessage history = %+v, want only the iMessage turn", imMsgs)
	}
}

// TestHistoryTransportPrefix proves the filter used by view_all_history maps a
// requested channel onto the right key prefix and treats "all" as unfiltered.
func TestHistoryTransportPrefix(t *testing.T) {
	cases := map[string]string{
		"imessage":   "imessage:",
		"IMESSAGE":   "imessage:",
		"whatsapp":   "",
		"":           "",
		"all":        "",
		"nonsense":   "",
		"  imessage": "imessage:",
	}
	for in, want := range cases {
		if got := historyTransportPrefix(in); got != want {
			t.Errorf("historyTransportPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
