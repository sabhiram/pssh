package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/crypto/ssh"
)

func fatalOnError(err error) {
	if err != nil {
		fmt.Printf("Fatal error: %s\n", err.Error())
		os.Exit(1)
	}
}

type sshaddr struct {
	user string
	pass string
	host string
	port int
}

// ParseSSHAddr accepts a string of the form: `user[:pass]@host[:port]` and
// populates a sshaddr instance with the appropriate fields populated.  If the
// port is omitted, it will default to `22`.
// TODO: Does not handle ssh host with no username.
func ParseSSHAddr(s string) (sshaddr, error) {
	var ret sshaddr

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
			return ret, fmt.Errorf("invalid port (%s)", hp[1])
		}
		ret.port = p
	default:
		return ret, errors.New("invalid host address specified")
	}

	return ret, nil
}

func (s sshaddr) User() string { return s.user }
func (s sshaddr) Pass() string { return s.pass }
func (s sshaddr) Host() string { return s.host }
func (s sshaddr) Port() int    { return s.port }

type Client struct {
	conn   ssh.Conn
	config *ssh.ClientConfig
	sess   *ssh.Session
	watch  *fsnotify.Watcher
}

func NewClient(addr string) (*Client, error) {
	watch, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	ssha, err := ParseSSHAddr(addr)
	if err != nil {
		return nil, err
	}

	config := &ssh.ClientConfig{
		User: ssha.User(),
		Auth: []ssh.AuthMethod{
			ssh.Password(ssha.Pass()),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	sshAddr := fmt.Sprintf("%s:%d", ssha.Host(), ssha.Port())
	conn, err := ssh.Dial("tcp", sshAddr, config)
	if err != nil {
		return nil, err
	}

	sess, err := conn.NewSession()
	if err != nil {
		return nil, err
	}

	return &Client{
		conn:   conn,
		config: config,
		sess:   sess,
		watch:  watch,
	}, nil
}

func (c *Client) Start() error {
	stdout, err := c.sess.StdoutPipe()
	if err != nil {
		return err
	}
	go io.Copy(os.Stdout, stdout)

	stderr, err := c.sess.StderrPipe()
	if err != nil {
		return err
	}
	go io.Copy(os.Stderr, stderr)

	stdin, err := c.sess.StdinPipe()
	if err != nil {
		return err
	}
	go io.Copy(stdin, os.Stdin)

	term_modes := ssh.TerminalModes{
		ssh.ECHO:  0,
		ssh.IGNCR: 1,
	}

	err = c.sess.RequestPty("xterm", 80, 40, term_modes)
	if err != nil {
		return err
	}

	err = c.sess.Shell()
	if err != nil {
		return err
	}

	for {
		select {
		case evt := <-c.watch.Events:
			fmt.Printf("Got event %#v\n", evt)
			if evt.Op&fsnotify.Write == fsnotify.Write {
				fmt.Printf("  Wrote file %s\n", evt.Name)
			}
		case err := <-c.watch.Errors:
			fmt.Printf("Got error: %s\n", err.Error())
			return err
		}
	}

	return nil
}

func (c Client) SubscribeDir(dirpath string) error {
	return c.watch.Add(dirpath)
}

func (c *Client) Close() {
	c.sess.Close()
	c.conn.Close()
	c.watch.Close()
}

func main() {
	client, err := NewClient(os.Args[1])
	fatalOnError(err)
	defer client.Close()

	client.SubscribeDir(".")

	go client.Start()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	func() {
		for {
			<-c
			fmt.Printf("Got Ctrl+C\n")
			os.Exit(1)
		}
	}()
}

func init() {
	flag.Parse()
}
