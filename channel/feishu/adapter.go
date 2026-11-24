package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"pi-go/channel"
)

// Emoji keys are the platform's own vocabulary; hermes uses the same two for
// the same meaning. A wrong key costs one failed call and nothing else —
// every reaction path is best-effort.
const (
	emojiTyping   = "Typing"
	emojiCross    = "CrossMark"
	eventQueueCap = 256
	ackTimeout    = 3 * time.Second // the platform's own processing window
)

// Adapter is the Feishu transport: a WebSocket long connection inbound, typed
// REST outbound, and the admission rules channel-feishu.md records.
type Adapter struct {
	cfg       Config
	client    *lark.Client
	events    chan *larkim.P2MessageReceiveV1
	logger    *log.Logger
	allowlist *channel.Allowlist
	pairing   *channel.Pairing

	mu         sync.Mutex
	selfOpenID string
	reactions  map[string]string // inbound message id → the Typing reaction we own
	onStatus   func(state, detail string)
}

func New(cfg Config, allowlist *channel.Allowlist, pairing *channel.Pairing, logger *log.Logger) (*Adapter, error) {
	if logger == nil {
		logger = log.Default()
	}
	return &Adapter{
		cfg:       cfg,
		client:    lark.NewClient(cfg.AppID, cfg.AppSecret, lark.WithLogLevel(larkcore.LogLevelWarn)),
		events:    make(chan *larkim.P2MessageReceiveV1, eventQueueCap),
		logger:    logger,
		allowlist: allowlist,
		pairing:   pairing,
		reactions: map[string]string{},
	}, nil
}

func (a *Adapter) Name() string { return "feishu" }

// SetStatusHandler registers an optional callback notified as the WebSocket
// connection transitions between connected, reconnecting, and error states.
func (a *Adapter) SetStatusHandler(fn func(state, detail string)) {
	a.mu.Lock()
	a.onStatus = fn
	a.mu.Unlock()
}

func (a *Adapter) emitStatus(state, detail string) {
	a.mu.Lock()
	fn := a.onStatus
	a.mu.Unlock()
	if fn != nil {
		fn(state, detail)
	}
}

func (a *Adapter) Caps() channel.Capabilities {
	return channel.Capabilities{MaxRunes: channel.DefaultChunkRunes}
}

// Run holds the long connection for the life of the process. The SDK
// reconnects by itself; Start returns only when the context ends or the
// connection gives up for good.
func (a *Adapter) Run(ctx context.Context, sink channel.Sink) error {
	a.hydrateSelf(ctx)

	handler := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(
		func(ctx context.Context, ev *larkim.P2MessageReceiveV1) error {
			// The platform acks after this function returns, and gives the
			// handler three seconds. Processing happens on the adapter loop
			// below; here the event is only handed off. Blocking is the
			// honest backpressure: a late ack makes the platform redeliver,
			// and the gateway's deduper absorbs the copy.
			select {
			case a.events <- ev:
			case <-ctx.Done():
			}
			return nil
		})

	wsClient := larkws.NewClient(a.cfg.AppID, a.cfg.AppSecret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelWarn))
	wsClient.SetOnReady(func() {
		a.logger.Printf("feishu: 长连接已就绪")
		a.emitStatus("connected", "")
	})
	wsClient.SetOnReconnecting(func() {
		a.logger.Printf("feishu: 正在重连")
		a.emitStatus("reconnecting", "")
	})
	wsClient.SetOnReconnected(func() {
		a.logger.Printf("feishu: 长连接已恢复")
		a.emitStatus("connected", "")
	})
	wsClient.SetOnError(func(err error) {
		a.logger.Printf("feishu: 连接错误: %v", err)
		if err != nil {
			a.emitStatus("error", err.Error())
		}
	})

	done := make(chan error, 1)
	go func() { done <- wsClient.Start(ctx) }()

	for {
		select {
		case ev := <-a.events:
			a.process(ctx, ev, sink)
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				return err
			}
			return nil
		case <-ctx.Done():
			wsClient.Close()
			return nil
		}
	}
}

// hydrateSelf fetches the bot's own open_id, which is what a group @ resolves
// against. A failure is survivable: the mention gate falls back to counting
// any bot mention, which is wrong only in rooms with two bots.
func (a *Adapter) hydrateSelf(ctx context.Context) {
	resp, err := a.client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		a.logger.Printf("feishu: bot/v3/info failed: %v", err)
		return
	}
	var body struct {
		Bot struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &body); err != nil || body.Bot.OpenID == "" {
		a.logger.Printf("feishu: bot/v3/info unreadable (err=%v)", err)
		return
	}
	a.mu.Lock()
	a.selfOpenID = body.Bot.OpenID
	a.mu.Unlock()
}

// process normalizes one queued event and hands it to the gateway, or answers
// it with the one line the admission rules owe the reader.
func (a *Adapter) process(ctx context.Context, ev *larkim.P2MessageReceiveV1, sink channel.Sink) {
	defer func() {
		if r := recover(); r != nil {
			a.logger.Printf("feishu: event processing panicked: %v", r)
		}
	}()
	data := ev.Event
	if data == nil || data.Message == nil || data.Message.MessageId == nil ||
		data.Message.ChatId == nil || data.Sender == nil || data.Sender.SenderId == nil {
		a.logger.Printf("feishu: dropping malformed event (missing message/sender)")
		return
	}
	m := data.Message
	messageID := *m.MessageId
	chatID := *m.ChatId

	chatType := channel.ChatDM
	if deref(m.ChatType) != "p2p" {
		chatType = channel.ChatGroup
	}
	userID, _ := senderID(data.Sender)
	target := channel.Target{
		Adapter:  a.Name(),
		ChatID:   chatID,
		ThreadID: deref(m.ThreadId),
		ChatType: chatType,
		UserID:   userID,
		UserName: userID,
	}

	if ok, reply := a.admit(data.Sender, m); !ok {
		if reply != "" {
			a.Send(ctx, target, reply, messageID)
		}
		return
	}

	text, kind, ok := a.content(m)
	if !ok {
		a.Send(ctx, target, "暂不支持"+kind+"消息，请发文字。", messageID)
		return
	}
	text = a.resolveMentions(m, text)
	if strings.TrimSpace(text) == "" {
		return // a bare @bot is a nudge, not a prompt
	}
	sink.Inbound(channel.Inbound{
		Target:    target,
		MessageID: messageID,
		Text:      text,
		At:        time.Now(),
	})
}

// admit is the admission ladder, in the order channel-feishu.md records:
// bot senders, then per-group policy and mention gating, then DM policy.
// Every rejection that cost the reader nothing stays silent; every rejection
// the reader acted on gets the one line that says how to get in.
func (a *Adapter) admit(sender *larkim.EventSender, m *larkim.EventMessage) (ok bool, reply string) {
	_, isBot := senderID(sender)
	if isBot && !a.cfg.AllowBots {
		return false, ""
	}
	if deref(m.ChatType) == "p2p" {
		return a.admitDM(sender, m)
	}

	mentioned := a.mentionsSelf(m)
	rule := a.cfg.Groups[deref(m.ChatId)]
	requireMention := true
	if a.cfg.RequireMention != nil {
		requireMention = *a.cfg.RequireMention
	}
	if rule.RequireMention != nil {
		requireMention = *rule.RequireMention
	}
	if requireMention && !mentioned {
		return false, ""
	}
	admin := a.inIDs(sender, a.cfg.Admins)
	switch strings.ToLower(policyOr(a.cfg.GroupPolicy, rule.Policy)) {
	case "disabled":
		return false, ""
	case "admin_only":
		return admin, ""
	case "open":
		return true, ""
	default: // allowlist
		if admin || a.inIDs(sender, rule.AllowedUsers) || a.inIDs(sender, a.cfg.AllowedUsers) || a.inAllowlist(sender) {
			return true, ""
		}
		return false, ""
	}
}

func (a *Adapter) admitDM(sender *larkim.EventSender, m *larkim.EventMessage) (bool, string) {
	userID, _ := senderID(sender)
	if a.inIDs(sender, a.cfg.Admins) || a.inIDs(sender, a.cfg.AllowedUsers) || a.inAllowlist(sender) {
		return true, ""
	}
	switch strings.ToLower(a.cfg.DMPolicy) {
	case "disabled":
		return false, ""
	case "open":
		return true, ""
	case "allowlist":
		return false, "你（" + userID + "）不在白名单里，请管理员把它加进 feishu.allowedUsers。"
	default: // pairing
		code, err := a.pairing.NewCode(a.Name(), userID, deref(m.ChatId))
		if err != nil {
			a.logger.Printf("pairing code for %s failed: %v", userID, err)
			return false, "配对暂时不可用，请稍后再试。"
		}
		return false, "还没有配对。配对码 " + code + "\n" +
			"在网关所在的机器上执行：\n" +
			"  atlas-gateway approve " + code + "\n" +
			"然后重新发一条消息。"
	}
}

// senderID picks the stable id the allowlists speak: open_id first, union_id
// as the fallback. It also reports whether the sender is itself a bot.
func senderID(sender *larkim.EventSender) (string, bool) {
	if sender == nil || sender.SenderId == nil {
		return "", false
	}
	id := sender.SenderId
	userID := firstNonEmpty(deref(id.OpenId), deref(id.UnionId), deref(id.UserId))
	isBot := sender.SenderType != nil && (*sender.SenderType == "bot" || *sender.SenderType == "app")
	return userID, isBot
}

// inIDs reports whether any of the sender's ids appears in ids.
func (a *Adapter) inIDs(sender *larkim.EventSender, ids []string) bool {
	if sender == nil || sender.SenderId == nil || len(ids) == 0 {
		return false
	}
	id := sender.SenderId
	for _, candidate := range []string{deref(id.OpenId), deref(id.UnionId), deref(id.UserId)} {
		for _, want := range ids {
			if candidate != "" && candidate == want {
				return true
			}
		}
	}
	return false
}

func (a *Adapter) inAllowlist(sender *larkim.EventSender) bool {
	if a.allowlist == nil {
		return false
	}
	id := sender.SenderId
	for _, candidate := range []string{deref(id.OpenId), deref(id.UnionId), deref(id.UserId)} {
		if candidate != "" && a.allowlist.Has(a.Name(), candidate) {
			return true
		}
	}
	return false
}

// mentionsSelf resolves the @ gate against the bot's own open_id. Without it
// (hydration failed), a bot mention is assumed — one bot per room is the
// common case, and the log says which mode is in effect.
func (a *Adapter) mentionsSelf(m *larkim.EventMessage) bool {
	self := a.selfID()
	for _, mention := range m.Mentions {
		if mention == nil || deref(mention.MentionedType) != "bot" || mention.Id == nil {
			continue
		}
		if self == "" {
			a.logger.Printf("feishu: self open_id unknown; treating a bot mention as the gate")
			return true
		}
		if deref(mention.Id.OpenId) == self {
			return true
		}
	}
	return false
}

// resolveMentions turns the event's @_user_N placeholders back into names,
// dropping the bot's own so "@atlas 帮我看看" arrives as "帮我看看".
func (a *Adapter) resolveMentions(m *larkim.EventMessage, text string) string {
	self := a.selfID()
	for _, mention := range m.Mentions {
		if mention == nil || mention.Key == nil {
			continue
		}
		name := "@" + deref(mention.Name)
		if mention.Id != nil && self != "" && deref(mention.Id.OpenId) == self {
			name = ""
		}
		text = strings.ReplaceAll(text, deref(mention.Key), name)
	}
	return strings.TrimSpace(text)
}

func (a *Adapter) selfID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.selfOpenID
}

// content decodes the message body the way the event's msg_type asks. Text
// and post cover what a reader types; everything else is named in the reply
// rather than silently dropped.
func (a *Adapter) content(m *larkim.EventMessage) (text, kind string, ok bool) {
	kind = deref(m.MessageType)
	switch kind {
	case "text":
		var body struct {
			Text string `json:"text"`
		}
		ok = bodyBytes(m.Content, &body)
		if ok {
			text = body.Text
		}
		return text, kind, ok
	case "post":
		var body struct {
			Title   string        `json:"title"`
			Content [][]postParts `json:"content"`
		}
		if !bodyBytes(m.Content, &body) {
			return "", kind, false
		}
		var lines []string
		if strings.TrimSpace(body.Title) != "" {
			lines = append(lines, body.Title)
		}
		for _, line := range body.Content {
			var parts []string
			for _, element := range line {
				switch element.Tag {
				case "text", "a":
					if element.Text != "" {
						parts = append(parts, element.Text)
					}
				case "img":
					parts = append(parts, "[图片]")
				case "at":
					parts = append(parts, "@")
				}
			}
			if joined := strings.TrimSpace(strings.Join(parts, "")); joined != "" {
				lines = append(lines, joined)
			}
		}
		return strings.Join(lines, "\n"), kind, true
	default:
		return "", kind, false
	}
}

type postParts struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
}

func bodyBytes(content *string, into any) bool {
	if content == nil || *content == "" {
		return false
	}
	return json.Unmarshal([]byte(*content), into) == nil
}

// Send delivers the answer, downgraded to what a Feishu text bubble renders,
// chunked, with the reader's own message quoted under the first piece.
func (a *Adapter) Send(ctx context.Context, t channel.Target, text string, replyTo string) error {
	for i, chunk := range channel.Chunk(Downgrade(text), channel.DefaultChunkRunes) {
		var err error
		if i == 0 && replyTo != "" {
			err = a.replyMessage(ctx, replyTo, chunk)
		} else {
			err = a.createMessage(ctx, t.ChatID, chunk)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) createMessage(ctx context.Context, chatID, text string) error {
	body, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return err
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(body)).
			Build()).
		Build()
	resp, err := a.client.Im.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu send: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu send: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (a *Adapter) replyMessage(ctx context.Context, messageID, text string) error {
	body, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return err
	}
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("text").
			Content(string(body)).
			Build()).
		Build()
	resp, err := a.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu reply: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu reply: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// ReactStart marks the reader's message as being worked on. It is the
// closest thing Feishu has to a typing indicator from a bot.
func (a *Adapter) ReactStart(ctx context.Context, t channel.Target, messageID string) {
	resp, err := a.client.Im.MessageReaction.Create(ctx,
		larkim.NewCreateMessageReactionReqBuilder().
			MessageId(messageID).
			Body(larkim.NewCreateMessageReactionReqBodyBuilder().
				ReactionType(&larkim.Emoji{EmojiType: strPtr(emojiTyping)}).
				Build()).
			Build())
	if err != nil || !resp.Success() {
		a.logger.Printf("feishu: reaction on %s failed (err=%v) — continuing", messageID, err)
		return
	}
	if resp.Data != nil && resp.Data.ReactionId != nil {
		a.mu.Lock()
		a.reactions[messageID] = *resp.Data.ReactionId
		a.mu.Unlock()
	}
}

// ReactEnd removes our Typing mark, and on a failure pins CrossMark so the
// reader who scrolled away knows the turn died without opening the log.
func (a *Adapter) ReactEnd(ctx context.Context, t channel.Target, messageID string, failed bool) {
	a.mu.Lock()
	reactionID, ok := a.reactions[messageID]
	delete(a.reactions, messageID)
	a.mu.Unlock()
	if ok && reactionID != "" {
		_, err := a.client.Im.MessageReaction.Delete(ctx,
			larkim.NewDeleteMessageReactionReqBuilder().
				MessageId(messageID).
				ReactionId(reactionID).
				Build())
		if err != nil {
			a.logger.Printf("feishu: reaction clear on %s failed: %v", messageID, err)
		}
	}
	if failed {
		_, err := a.client.Im.MessageReaction.Create(ctx,
			larkim.NewCreateMessageReactionReqBuilder().
				MessageId(messageID).
				Body(larkim.NewCreateMessageReactionReqBodyBuilder().
					ReactionType(&larkim.Emoji{EmojiType: strPtr(emojiCross)}).
					Build()).
				Build())
		if err != nil {
			a.logger.Printf("feishu: CrossMark on %s failed: %v", messageID, err)
		}
	}
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func policyOr(defaultPolicy, override string) string {
	if override != "" {
		return override
	}
	return defaultPolicy
}

func strPtr(v string) *string { return &v }
