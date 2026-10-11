package domain

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

func Clean(s string, max int) string {
	r := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "�")))
	if len(r) > max {
		r = r[:max]
	}
	return string(r)
}
func Decide(e MailEvent, alias string, rules []Rule) Decision {
	d := Decision{EventKey: e.Key}
	if e.Kind != "added" {
		return d
	}
	d.Notify = true
	selected := Rule{Action: "notify", Priority: 3}
	rs := append([]Rule(nil), rules...)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Order < rs[j].Order })
	for _, r := range rs {
		if r.AccountID != "" && r.AccountID != e.AccountID {
			continue
		}
		if r.SenderAddress != "" && !strings.EqualFold(r.SenderAddress, e.SenderAddress) {
			continue
		}
		at := strings.LastIndex(e.SenderAddress, "@")
		if r.SenderDomain != "" && (at < 0 || !strings.EqualFold(e.SenderAddress[at+1:], r.SenderDomain)) {
			continue
		}
		if r.SubjectContains != "" && !strings.Contains(strings.ToLower(e.Subject), strings.ToLower(r.SubjectContains)) {
			continue
		}
		selected = r
		break
	}
	if selected.Action == "ignore" {
		d.Notify = false
		return d
	}
	p := selected.Priority
	if p < 1 || p > 5 {
		p = 3
	}
	subject := Clean(e.Subject, 500)
	if subject == "" {
		subject = "（无主题）"
	}
	if selected.HideSubject {
		subject = "（主题已隐藏）"
	}
	sender := Clean(e.SenderName+" <"+e.SenderAddress+">", 200)
	if selected.HideSender {
		sender = "（发件人已隐藏）"
	}
	click := ""
	u, err := url.Parse(e.ClickURL)
	if err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil {
		switch strings.ToLower(u.Hostname()) {
		case "mail.google.com", "mail.qq.com", "outlook.live.com", "outlook.office.com", "outlook.office365.com":
			click = u.String()
		}
	}
	d.Payload = Notification{Title: "新邮件 · " + Clean(alias, 60), Message: fmt.Sprintf("发件人：%s\n主题：%s\n接收时间：%s", sender, subject, e.ReceivedAt.UTC().Format("2006-01-02 15:04:05 UTC")), Priority: p, Click: click}
	return d
}
