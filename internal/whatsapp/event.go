package whatsapp

import (
	"context"
	_ "embed"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/beeep"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

//go:embed utils/clark.png
var clarkIcon []byte

var (
	sentIDsMu sync.Mutex
	sentIDs   = make(map[string]struct{})
)

func markSent(id string) {
	if id == "" {
		return
	}
	sentIDsMu.Lock()
	defer sentIDsMu.Unlock()
	sentIDs[id] = struct{}{}
}

func wasSent(id string) bool {
	sentIDsMu.Lock()
	defer sentIDsMu.Unlock()
	_, ok := sentIDs[id]
	if ok {
		delete(sentIDs, id)
	}
	return ok
}

func sendSelf(cli *whatsmeow.Client, msg string) {
	targetJID := cli.Store.ID.ToNonAD()

	resp, err := cli.SendMessage(context.Background(), targetJID, &waE2E.Message{
		Conversation: proto.String(msg),
	})
	if err != nil {
		Log("WHATSAPP", SevErr, "SEND", "Failed to send self message", "error", err)
		return
	}
	markSent(string(resp.ID))
}

func reply(cli *whatsmeow.Client, to types.JID, msg string) {
	resp, err := cli.SendMessage(context.Background(), to, &waE2E.Message{
		Conversation: proto.String(msg),
	})
	if err != nil {
		Log("WHATSAPP", SevErr, "SEND", "Failed to send message", "to", to.User, "error", err)
		return
	}
	markSent(string(resp.ID))
	logReply(to.User, msg)
}

func resolveSender(cli *whatsmeow.Client, v *events.Message) types.JID {
	sender := v.Info.Sender.ToNonAD()
	if sender.Server == types.HiddenUserServer {
		if pn, err := cli.Store.LIDs.GetPNForLID(context.Background(), sender); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD()
		} else if v.Info.IsFromMe && cli.Store.ID != nil {
			return cli.Store.ID.ToNonAD()
		}
	}
	return sender
}

func isSelfChat(cli *whatsmeow.Client, chat types.JID) bool {
	if cli == nil || cli.Store == nil || cli.Store.ID == nil {
		return false
	}

	self := cli.Store.ID.ToNonAD()
	if self.Server == types.HiddenUserServer {
		if pn, err := cli.Store.LIDs.GetPNForLID(context.Background(), self); err == nil && !pn.IsEmpty() {
			self = pn.ToNonAD()
		}
	}

	chat = chat.ToNonAD()
	if chat.Server == types.HiddenUserServer {
		if pn, err := cli.Store.LIDs.GetPNForLID(context.Background(), chat); err == nil && !pn.IsEmpty() {
			chat = pn.ToNonAD()
		}
	}

	return chat.User == self.User && chat.Server == self.Server
}

func EventHandler(waClient *whatsmeow.Client, ast *Assistant, connectedAt time.Time) whatsmeow.EventHandler {
	return func(evt any) {
		switch v := evt.(type) {
		case *events.Message:
			if v == nil || v.Info.Chat.IsEmpty() || v.Info.Sender.IsEmpty() || v.Message == nil {
				Log("WHATSAPP", SevWarn, "MESSAGE", "Message discarded", "reason", "nil message data")
				return
			}

			if wasSent(string(v.Info.ID)) {
				return
			}

			if v.Info.Timestamp.IsZero() || v.Info.Timestamp.Before(connectedAt) {
				return
			}

			if v.Info.IsFromMe && !isSelfChat(waClient, v.Info.Chat) {
				return
			}

			sender := resolveSender(waClient, v)
			senderStr := sender.String()
			relation, isVIP := ast.VIP.CheckVIP(senderStr)

			var userMsg string
			if conversation := v.Message.GetConversation(); conversation != "" {
				userMsg = conversation
			} else if extendedMessage := v.Message.GetExtendedTextMessage(); extendedMessage != nil {
				userMsg = extendedMessage.GetText()
			}

			who := "Unknown"
			if isVIP {
				who = relation
			}
			logIncoming(v, sender, who, isVIP, userMsg)

			if !ast.Status || !isVIP || v.Info.IsGroup {
				return
			}

			if userMsg == "" {
				Log("WHATSAPP", SevWarn, "MESSAGE", "Message discarded", "reason", "no text content")
				return
			}

			if strings.Contains(strings.ToLower(userMsg), "get him to me") {
				beeep.Notify("Attention Sir!", relation+" needs you!", clarkIcon)
				sendSelf(waClient, "🚨 Attention Master!\n"+relation+" needs you!")
				reply(waClient, sender, "I've alerted him. One Moment.")
				return
			}

			aiResp, err := ast.GetAIResponse(senderStr, userMsg)
			if err != nil {
				Log("OLLAMA", SevErr, "RESPONSE", "AI response error", "error", err)
				reply(waClient, sender, "I apologize, but I'm experiencing technical difficulties. Please try again later.")
				return
			}
			reply(waClient, sender, aiResp)
		}
	}
}
