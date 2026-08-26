package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- capability fakes -------------------------------------------------------

type describingButler struct {
	fakeButler
	desc     string
	err      error
	gotItems []MediaAttachment
}

func (b *describingButler) Describe(_ context.Context, items []MediaAttachment) (string, error) {
	b.gotItems = items
	if b.err != nil {
		return "", b.err
	}
	return b.desc, nil
}

type transcribingButler struct {
	fakeButler
	text   string
	err    error
	gotMIM string
}

func (b *transcribingButler) TranscribeVoice(_ context.Context, mime string, _ []byte) (string, error) {
	b.gotMIM = mime
	if b.err != nil {
		return "", b.err
	}
	return b.text, nil
}

type digestingButler struct {
	fakeButler
	digest  string
	err     error
	gotName string
}

func (b *digestingButler) DigestDocument(_ context.Context, name, _ string) (string, error) {
	b.gotName = name
	if b.err != nil {
		return "", b.err
	}
	return b.digest, nil
}

// --- helpers ----------------------------------------------------------------

func mediaMsg(sender, caption, mediaType string, atts []MediaAttachment) Message {
	m := Message{Sender: sender, Chat: sender, Text: caption, MediaType: mediaType, Media: atts}
	return m
}

func sentText(t *testing.T, msgr *fakeMessenger) string {
	t.Helper()
	msgr.mu.Lock()
	defer msgr.mu.Unlock()
	if len(msgr.sentTo) == 0 {
		t.Fatalf("no message sent")
	}
	// fakeMessenger records only the chat; replies are captured by butler.
	return msgr.sentTo[0]
}

// --- tests ------------------------------------------------------------------

func TestMediaUncaptionedImageDescribedAndAnswered(t *testing.T) {
	msgr := &fakeMessenger{}
	b := &describingButler{fakeButler: fakeButler{enabled: true}, desc: "a red sports car"}
	h := newTestHandler(msgr, b)

	h.Handle(mediaMsg(testVIP, "", "image", []MediaAttachment{{Type: "image", MIME: "image/jpeg", Data: []byte("jpg")}}))
	h.Close()

	if len(b.replied) != 1 {
		t.Fatalf("replies = %d, want 1", len(b.replied))
	}
	if !strings.Contains(b.replied[0], "[image: a red sports car]") {
		t.Errorf("reply %q missing described context", b.replied[0])
	}
	if len(b.gotItems) != 1 || b.gotItems[0].Type != "image" {
		t.Errorf("Describe items = %+v", b.gotItems)
	}
}

func TestMediaCaptionedVideoFallsBackToCaptionWithNotice(t *testing.T) {
	msgr := &fakeMessenger{}
	b := &describingButler{fakeButler: fakeButler{enabled: true}, err: errors.New("e4b down")}
	h := newTestHandler(msgr, b)

	h.Handle(mediaMsg(testVIP, "look at this", "video", []MediaAttachment{
		{Type: "video", MIME: "image/jpeg", Data: []byte("f1")},
		{Type: "video", MIME: "image/jpeg", Data: []byte("f2")},
	}))
	h.Close()

	if len(b.replied) != 1 {
		t.Fatalf("replies = %d, want 1 (caption-only path)", len(b.replied))
	}
	if strings.Contains(b.replied[0], "[video:") {
		t.Errorf("caption-only reply must not contain visual context: %q", b.replied[0])
	}
	if !strings.Contains(b.replied[0], "look at this") {
		t.Errorf("caption lost: %q", b.replied[0])
	}
	if len(msgr.sentTo) == 0 {
		t.Fatal("asset-failure notice was not sent")
	}
}

func TestMediaUncaptionedAudioWithoutTranscriberAcks(t *testing.T) {
	msgr := &fakeMessenger{}
	b := &fakeButler{enabled: true} // no AudioTranscriber capability
	h := newTestHandler(msgr, b)

	h.Handle(mediaMsg(testVIP, "", "audio", []MediaAttachment{{Type: "audio", MIME: "audio/ogg", Data: []byte("ogg")}}))
	h.Close()

	if len(b.replied) != 0 {
		t.Fatalf("model must not be invoked on ack path, got %v", b.replied)
	}
	sentText(t, msgr) // an ack went out
}

func TestMediaVoiceNoteTranscribedIntoContext(t *testing.T) {
	msgr := &fakeMessenger{}
	b := &transcribingButler{fakeButler: fakeButler{enabled: true}, text: "meeting moved to 5pm"}
	h := newTestHandler(msgr, b)

	h.Handle(mediaMsg(testVIP, "", "audio", []MediaAttachment{{Type: "audio", MIME: "audio/ogg", Data: []byte("ogg")}}))
	h.Close()

	if len(b.replied) != 1 {
		t.Fatalf("replies = %d, want 1", len(b.replied))
	}
	if !strings.Contains(b.replied[0], "[voice note: meeting moved to 5pm]") {
		t.Errorf("reply missing transcript context: %q", b.replied[0])
	}
	if b.gotMIM != "audio/ogg" {
		t.Errorf("mime passed through = %q", b.gotMIM)
	}
}

func TestMediaDigestDocumentCarriesFilename(t *testing.T) {
	msgr := &fakeMessenger{}
	b := &digestingButler{fakeButler: fakeButler{enabled: true}, digest: "## Purpose\nQ3 budget"}
	h := newTestHandler(msgr, b)

	h.Handle(mediaMsg(testVIP, "thoughts?", "document", []MediaAttachment{
		{Name: "report.pdf", Type: "document", MIME: "application/pdf", Data: []byte("extracted text")},
	}))
	h.Close()

	if len(b.replied) != 1 {
		t.Fatalf("replies = %d, want 1", len(b.replied))
	}
	if b.gotName != "report.pdf" {
		t.Errorf("digest name = %q, want report.pdf", b.gotName)
	}
	if !strings.Contains(b.replied[0], "thoughts?") || !strings.Contains(b.replied[0], "compacted") {
		t.Errorf("caption+digest not co-processed: %q", b.replied[0])
	}
}

func TestMediaProcessingOrderAudioDocVisual(t *testing.T) {
	type combo interface {
		Butler
	}
	_ = combo(nil)
	// A butler implementing all three capabilities verifies ordering rules:
	// audio first, docs second, visuals last — via line order in the reply.
	msgr := &fakeMessenger{}

	b := &fullMediaButler{fakeButler: fakeButler{enabled: true}}
	h := newTestHandler(msgr, b)

	h.Handle(Message{
		Sender: testVIP, Chat: testVIP,
		MediaType: "image",
		Media: []MediaAttachment{
			{Type: "audio", MIME: "audio/ogg", Data: []byte("a")},
			{Name: "f.pdf", Type: "document", MIME: "application/pdf", Data: []byte("t")},
			{Type: "image", MIME: "image/jpeg", Data: []byte("i")},
		},
	})
	h.Close()

	if len(b.replied) != 1 {
		t.Fatalf("replies = %d, want 1", len(b.replied))
	}
	r := b.replied[0]
	ia, id, iv := strings.Index(r, "[voice note:"), strings.Index(r, "[document"), strings.Index(r, "[image:")
	if ia < 0 || id < 0 || iv < 0 {
		t.Fatalf("missing context lines: %q", r)
	}
	if !(ia < id && id < iv) {
		t.Errorf("context order wrong: voice@%d doc@%d visual@%d", ia, id, iv)
	}
}

type fullMediaButler struct {
	fakeButler
}

func (b *fullMediaButler) Describe(_ context.Context, _ []MediaAttachment) (string, error) {
	return "photo content", nil
}
func (b *fullMediaButler) TranscribeVoice(_ context.Context, _ string, _ []byte) (string, error) {
	return "spoken words", nil
}
func (b *fullMediaButler) DigestDocument(_ context.Context, _, _ string) (string, error) {
	return "condensed facts", nil
}
