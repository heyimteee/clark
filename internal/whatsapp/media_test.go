package whatsapp

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func mediaEvent(m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.JID{User: "6281234567890", Server: "s.whatsapp.net"},
				Sender: types.JID{User: "6281234567890", Server: "s.whatsapp.net"},
			},
			Timestamp: time.Now(),
		},
		Message: m,
	}
}

func TestExtractTextAndMediaGifDetection(t *testing.T) {
	v := mediaEvent(&waE2E.Message{
		VideoMessage: &waE2E.VideoMessage{
			Caption:     proto.String("loop"),
			GifPlayback: proto.Bool(true),
		},
	})
	text, kind := extractTextAndMedia(v)
	if text != "loop" || kind != "gif" {
		t.Errorf("got (%q,%q), want (loop,gif)", text, kind)
	}
}

func TestExtractTextAndMediaPlainVideo(t *testing.T) {
	v := mediaEvent(&waE2E.Message{
		VideoMessage: &waE2E.VideoMessage{Caption: proto.String("clip")},
	})
	if _, kind := extractTextAndMedia(v); kind != "video" {
		t.Errorf("kind = %q, want video", kind)
	}
}

func TestExtractTextAndMediaSticker(t *testing.T) {
	v := mediaEvent(&waE2E.Message{
		StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("image/webp")},
	})
	if _, kind := extractTextAndMedia(v); kind != "sticker" {
		t.Errorf("kind = %q, want sticker", kind)
	}
}

func TestStickerAnimated(t *testing.T) {
	animated := mediaEvent(&waE2E.Message{
		StickerMessage: &waE2E.StickerMessage{
			Mimetype:   proto.String("image/webp"),
			IsAnimated: proto.Bool(true),
		},
	})
	if !stickerAnimated(animated) {
		t.Error("IsAnimated=true not detected")
	}
	videoSticker := mediaEvent(&waE2E.Message{
		StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("video/mp4")},
	})
	if !stickerAnimated(videoSticker) {
		t.Error("video/* sticker not detected as animated")
	}
	static := mediaEvent(&waE2E.Message{
		StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("image/webp")},
	})
	if stickerAnimated(static) {
		t.Error("static webp flagged animated")
	}
}

func TestMediaKindFor(t *testing.T) {
	cases := map[string]string{
		"audio": "audio", "video": "video", "gif": "video",
		"sticker": "sticker", "image": "image", "document": "document",
	}
	for in, want := range cases {
		if got := mediaKindFor(in); got != want {
			t.Errorf("mediaKindFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsTextLike(t *testing.T) {
	yes := [][2]string{
		{"text/plain", ""},
		{"application/json", ""},
		{"", "notes.md"},
		{"", "data.csv"},
		{"application/octet-stream", "main.go"},
	}
	no := [][2]string{
		{"application/zip", "bundle.zip"},
		{"application/msword", "doc.doc"},
	}
	for _, c := range yes {
		if !isTextLike(c[0], c[1]) {
			t.Errorf("isTextLike(%q,%q) = false, want true", c[0], c[1])
		}
	}
	for _, c := range no {
		if isTextLike(c[0], c[1]) {
			t.Errorf("isTextLike(%q,%q) = true, want false", c[0], c[1])
		}
	}
}
