package sshaddr

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

////////////////////////////////////////////////////////////////////////////////

// SSHAddr wraps the needed info to connect to a remote SSH session.
type SSHAddr struct {
	user string // Username
	pass string // User password [optional]
	host string // Hostname / address to connect to
	port int    // Port to connect to [default=22]
	dest string // Destination directory
}

// Parse accepts a string of the form: `user[:pass]@host[:port][:destination]`
// and populates a sshaddr instance with the appropriate fields populated.  If
// the port is omitted, it will default to `22`.
// TODO: Does not handle ssh host with no username.
func Parse(s string) (*SSHAddr, error) {
	ret := &SSHAddr{}

	ss := strings.Split(s, "@")
	if len(ss) != 2 {
		return ret, fmt.Errorf("malformed SSH address string (%s)", s)
	}

	up := strings.Split(ss[0], ":")
	switch len(up) {
	case 0:
		// nothing
	case 1:
		ret.user = up[0]
	default:
		ret.user = up[0]
		ret.pass = strings.Join(up[1:], ":")
	}

	ret.port = 22

	hp := strings.Split(ss[1], ":")
	switch len(hp) {
	case 1:
		ret.host = hp[0]
	case 2:
		ret.host = hp[0]
		p, err := strconv.Atoi(hp[1])
		if err != nil {
			ret.port = 22
			ret.dest = hp[1]
		} else {
			ret.port = p
		}
	case 3:
		ret.host = hp[0]
		p, err := strconv.Atoi(hp[1])
		if err != nil {
			return nil, err
		}
		ret.port = p
		ret.dest = hp[2]
	default:
		return ret, errors.New("invalid host address specified")
	}
	return ret, nil
}

func (s *SSHAddr) User() string {
	if s == nil {
		return ""
	}
	return s.user
}

func (s *SSHAddr) Pass() string {
	if s == nil {
		return ""
	}
	return s.pass
}

func (s *SSHAddr) Host() string {
	if s == nil {
		return "localhost"
	}
	return s.host
}

func (s *SSHAddr) Port() int {
	if s == nil {
		return 22
	}
	return s.port
}

func (s *SSHAddr) Destination() string {
	if s == nil {
		return ""
	}
	return s.dest
}
