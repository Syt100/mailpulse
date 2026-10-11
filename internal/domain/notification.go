package domain

import (
	"fmt"
	"net/url"
	"regexp"
)

var topicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (c NotificationConfig) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("invalid ntfy base URL")
	}
	if !topicPattern.MatchString(c.Topic) {
		return fmt.Errorf("invalid ntfy topic")
	}
	return nil
}
