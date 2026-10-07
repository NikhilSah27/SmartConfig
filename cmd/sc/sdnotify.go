package main

import (
	"net"
	"os"
)

// sdNotify sends state ("READY=1") to systemd's notify socket, when sc runs
// under systemd as a Type=notify service ($NOTIFY_SOCKET; a name that
// starts with "@" is in the abstract namespace, which net takes as it is).
// Without the variable it does nothing.
func sdNotify(state string) error {
	name := os.Getenv("NOTIFY_SOCKET")
	if name == "" {
		return nil
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte(state))
	return err
}
