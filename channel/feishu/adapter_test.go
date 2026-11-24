package feishu

import (
	"path/filepath"
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"pi-go/channel"
)

func testConfig(t *testing.T, mutate func(*Config)) *Adapter {
	t.Helper()
	cfg := Config{
		AppID: "cli_test", AppSecret: "secret",
		DMPolicy: "pairing", GroupPolicy: "allowlist",
		RequireMention: boolPtr(true),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	dir := t.TempDir()
	allowlist, err := channel.OpenAllowlist(filepath.Join(dir, "allowlist.json"))
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := channel.OpenPairing(filepath.Join(dir, "pairing.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(cfg, allowlist, pairing, nil)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func sdkSender(openID, senderType string) *larkim.EventSender {
	return &larkim.EventSender{
		SenderId:   &larkim.UserId{OpenId: strPtr(openID)},
		SenderType: strPtr(senderType),
	}
}

func sdkTextMessage(chatType, chatID string, mentions []*larkim.MentionEvent) *larkim.EventMessage {
	return &larkim.EventMessage{
		ChatType:    strPtr(chatType),
		ChatId:      strPtr(chatID),
		MessageType: strPtr("text"),
		Content:     strPtr(`{"text":"你好"}`),
		Mentions:    mentions,
	}
}

func botMention(openID string) []*larkim.MentionEvent {
	return []*larkim.MentionEvent{{
		Key:           strPtr("@_user_1"),
		MentionedType: strPtr("bot"),
		Id:            &larkim.UserId{OpenId: strPtr(openID)},
	}}
}

func TestAdmitBotSenderDropped(t *testing.T) {
	a := testConfig(t, nil)
	if ok, reply := a.admit(sdkSender("ou_bot", "bot"), sdkTextMessage("group", "oc_1", nil)); ok || reply != "" {
		t.Fatalf("bot sender admitted: %v/%q", ok, reply)
	}
}

func TestAdmitGroupRequiresMention(t *testing.T) {
	a := testConfig(t, nil)
	a.selfOpenID = "ou_self"
	if ok, _ := a.admit(sdkSender("ou_user", "user"), sdkTextMessage("group", "oc_1", nil)); ok {
		t.Fatal("group message without mention admitted")
	}
	// The mention gate answers to the bot's own open_id, and the group
	// allowlist starts empty: a mention alone does not admit a stranger.
	if ok, _ := a.admit(sdkSender("ou_user", "user"), sdkTextMessage("group", "oc_1", botMention("ou_self"))); ok {
		t.Fatal("mention bypassed the empty allowlist")
	}
}

func TestAdmitGroupOpenIgnoresMentionPerChat(t *testing.T) {
	a := testConfig(t, func(c *Config) {
		c.Groups = map[string]GroupRule{"oc_open": {Policy: "open", RequireMention: boolPtr(false)}}
	})
	a.selfOpenID = "ou_self"
	if ok, _ := a.admit(sdkSender("ou_user", "user"), sdkTextMessage("group", "oc_open", nil)); !ok {
		t.Fatal("per-chat open+no-mention rejected")
	}
}

func TestAdmitGroupAllowlistAndAdmin(t *testing.T) {
	a := testConfig(t, func(c *Config) {
		c.Admins = []string{"ou_admin"}
		c.AllowedUsers = []string{"ou_friend"}
	})
	a.selfOpenID = "ou_self"
	mentioned := sdkTextMessage("group", "oc_1", botMention("ou_self"))

	if ok, _ := a.admit(sdkSender("ou_friend", "user"), mentioned); !ok {
		t.Fatal("allowlisted user rejected")
	}
	if ok, _ := a.admit(sdkSender("ou_admin", "user"), mentioned); !ok {
		t.Fatal("admin rejected")
	}
	if ok, _ := a.admit(sdkSender("ou_stranger", "user"), mentioned); ok {
		t.Fatal("stranger admitted into allowlisted group")
	}

	// admin_only answers to admins only, regardless of the allowlist.
	a.cfg.GroupPolicy = "admin_only"
	if ok, _ := a.admit(sdkSender("ou_friend", "user"), mentioned); ok {
		t.Fatal("non-admin passed admin_only")
	}
	if ok, _ := a.admit(sdkSender("ou_admin", "user"), mentioned); !ok {
		t.Fatal("admin failed admin_only")
	}
}

func TestAdmitGroupDisabled(t *testing.T) {
	a := testConfig(t, func(c *Config) { c.GroupPolicy = "disabled" })
	a.selfOpenID = "ou_self"
	mentioned := sdkTextMessage("group", "oc_1", botMention("ou_self"))
	if ok, _ := a.admit(sdkSender("ou_admin", "user"), mentioned); ok {
		t.Fatal("disabled group admitted")
	}
}

func TestAdmitDMPaths(t *testing.T) {
	a := testConfig(t, func(c *Config) { c.AllowedUsers = []string{"ou_friend"} })
	if ok, _ := a.admit(sdkSender("ou_friend", "user"), sdkTextMessage("p2p", "oc_dm", nil)); !ok {
		t.Fatal("allowlisted friend rejected in DM")
	}

	// pairing: an unknown DM gets a code and the command that redeems it.
	ok, reply := a.admit(sdkSender("ou_new", "user"), sdkTextMessage("p2p", "oc_dm", nil))
	if ok || !strings.Contains(reply, "配对码") || !strings.Contains(reply, "atlas-gateway approve") {
		t.Fatalf("pairing reply = %v/%q", ok, reply)
	}

	// allowlist policy: the unknown DM is told exactly what to add.
	a.cfg.DMPolicy = "allowlist"
	ok, reply = a.admit(sdkSender("ou_new", "user"), sdkTextMessage("p2p", "oc_dm", nil))
	if ok || !strings.Contains(reply, "ou_new") || !strings.Contains(reply, "白名单") {
		t.Fatalf("allowlist denial = %v/%q", ok, reply)
	}

	// open: everyone passes; disabled: nobody does.
	a.cfg.DMPolicy = "open"
	if ok, _ := a.admit(sdkSender("ou_anyone", "user"), sdkTextMessage("p2p", "oc_dm", nil)); !ok {
		t.Fatal("open DM rejected")
	}
	a.cfg.DMPolicy = "disabled"
	if ok, _ := a.admit(sdkSender("ou_anyone", "user"), sdkTextMessage("p2p", "oc_dm", nil)); ok {
		t.Fatal("disabled DM admitted")
	}
}

func TestAdmitRuntimeAllowlist(t *testing.T) {
	a := testConfig(t, func(c *Config) { c.DMPolicy = "allowlist" })
	if ok, _ := a.admit(sdkSender("ou_runtime", "user"), sdkTextMessage("p2p", "oc_dm", nil)); ok {
		t.Fatal("runtime-unknown user admitted before approval")
	}
	if err := a.allowlist.Add("feishu", "ou_runtime"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.admit(sdkSender("ou_runtime", "user"), sdkTextMessage("p2p", "oc_dm", nil)); !ok {
		t.Fatal("approved user still rejected")
	}
}

func TestContentTextAndPost(t *testing.T) {
	a := testConfig(t, nil)
	text, kind, ok := a.content(&larkim.EventMessage{
		MessageType: strPtr("text"),
		Content:     strPtr(`{"text":"  你好 "}`),
	})
	if !ok || kind != "text" || text != "  你好 " {
		t.Fatalf("text content = %q/%s/%v", text, kind, ok)
	}

	post := `{"title":"标题","content":[[{"tag":"text","text":"第一行"},{"tag":"a","text":"链接"}],[{"tag":"img"}],[{"tag":"text","text":"第二行"}]]}`
	text, kind, ok = a.content(&larkim.EventMessage{MessageType: strPtr("post"), Content: strPtr(post)})
	if !ok || kind != "post" {
		t.Fatalf("post content not decoded: %v", ok)
	}
	if want := "标题\n第一行链接\n[图片]\n第二行"; text != want {
		t.Fatalf("post flatten = %q, want %q", text, want)
	}

	_, kind, ok = a.content(&larkim.EventMessage{MessageType: strPtr("file"), Content: strPtr(`{}`)})
	if ok || kind != "file" {
		t.Fatalf("unsupported kind handling = %s/%v", kind, ok)
	}
}

func TestResolveMentions(t *testing.T) {
	a := testConfig(t, nil)
	a.selfOpenID = "ou_self"
	m := &larkim.EventMessage{Mentions: []*larkim.MentionEvent{
		{Key: strPtr("@_user_1"), MentionedType: strPtr("bot"), Id: &larkim.UserId{OpenId: strPtr("ou_self")}},
		{Key: strPtr("@_user_2"), MentionedType: strPtr("user"), Id: &larkim.UserId{OpenId: strPtr("ou_x")}, Name: strPtr("小明")},
	}}
	if got := a.resolveMentions(m, "@_user_1 帮我 @_user_2 看看"); got != "帮我 @小明 看看" {
		t.Fatalf("resolveMentions = %q", got)
	}
}

func TestCapsDeclineChunking(t *testing.T) {
	a := testConfig(t, nil)
	if a.Caps().MaxRunes != channel.DefaultChunkRunes {
		t.Fatalf("caps = %+v", a.Caps())
	}
	if a.Name() != "feishu" {
		t.Fatalf("name = %q", a.Name())
	}
}
