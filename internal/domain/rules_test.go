package domain

import (
	"strings"
	"testing"
	"time"
)

func TestRulesFirstMatchAND(t *testing.T) {
	e := MailEvent{Key: "k", AccountID: "a", Kind: "added", SenderAddress: "person@example.com", Subject: "重要提醒"}
	rules := []Rule{{Order: 1, SenderDomain: "example.com", SubjectContains: "广告", Action: "ignore"}, {Order: 2, SenderDomain: "example.com", Action: "notify", Priority: 5}, {Order: 3, Action: "ignore"}}
	d := Decide(e, "工作", rules)
	if !d.Notify || d.Payload.Priority != 5 {
		t.Fatalf("%+v", d)
	}
	rules[0].SubjectContains = "提醒"
	if Decide(e, "工作", rules).Notify {
		t.Fatal("first match ignored")
	}
}
func TestChineseNotification(t *testing.T) {
	e := MailEvent{Key: "k", Kind: "added", Subject: "中文主题", SenderName: "张三", SenderAddress: "z@example.com", ReceivedAt: time.Now()}
	d := Decide(e, "工作", nil)
	if d.Payload.Priority != 3 || !strings.Contains(d.Payload.Message, "中文主题") || !strings.Contains(d.Payload.Title, "工作") {
		t.Fatalf("%+v", d)
	}
	d = Decide(e, "工作", []Rule{{Action: "notify", HideSubject: true, HideSender: true}})
	if strings.Contains(d.Payload.Message, "中文主题") || strings.Contains(d.Payload.Message, "example.com") {
		t.Fatal("privacy leak")
	}
}
