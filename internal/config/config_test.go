package config

import "testing"

func TestLogLevelValidation(t *testing.T) {
	for _, value := range []string{"debug", "info", "warn", "error"} {
		c := Default()
		c.LogLevel = value
		if e := c.Finalize(); e != nil {
			t.Fatal(e)
		}
	}
	c := Default()
	c.LogLevel = "invalid-sensitive-value"
	if e := c.Finalize(); e == nil {
		t.Fatal("invalid log level accepted")
	}
}
