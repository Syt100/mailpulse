package runtime

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

type Lock struct{ file *os.File }

func AcquireLock(path string) (*Lock, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("another MailPulse instance owns this database")
	}
	return &Lock{f}, nil
}
func (l *Lock) Close() error { unix.Flock(int(l.file.Fd()), unix.LOCK_UN); return l.file.Close() }
