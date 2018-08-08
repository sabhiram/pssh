package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
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
	*ssh.Client

	config *ssh.ClientConfig
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
	client, err := ssh.Dial("tcp", sshAddr, config)
	if err != nil {
		return nil, err
	}

	return &Client{
		Client: client,

		config: config,
		watch:  watch,
	}, nil
}

func (c *Client) StartShell() error {
	sess, err := c.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	go io.Copy(os.Stdout, stdout)

	stderr, err := sess.StderrPipe()
	if err != nil {
		return err
	}
	go io.Copy(os.Stderr, stderr)

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	go io.Copy(stdin, os.Stdin)

	term_modes := ssh.TerminalModes{
		ssh.ECHO:  0,
		ssh.IGNCR: 1,
	}

	fd := int(os.Stdin.Fd())
	w, h, err := terminal.GetSize(fd)
	if err != nil {
		return err
	}

	err = sess.RequestPty("xterm", h, w, term_modes)
	if err != nil {
		return err
	}

	err = sess.Shell()
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

// Copy creates a new session using the underlying ssh connection and copies
// the contents from the source reader into the destination path specified by
// `dstpath`.  The file's permissions and size are expected.
func (c *Client) Copy(src io.Reader, dstpath, perms string, sz int64) error {
	sess, err := c.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	file := path.Base(dstpath)
	dirp := path.Dir(dstpath)

	go func() {
		dst, err := sess.StdinPipe()
		if err != nil {
			return
		}
		defer dst.Close()

		// TODO: We should probably only copy `sz` number of bytes here.
		fmt.Fprintf(dst, "C%s %d %s\n", perms, sz, file)
		io.Copy(dst, src)
		fmt.Fprintf(dst, "\x00")
	}()

	return sess.Run("/usr/bin/scp -qt " + dirp)
}

func (c *Client) SubscribeDir(dirpath string) error {
	err := c.watch.Add(dirpath)
	if err != nil {
		return err
	}

	// TODO: List all relevant files and add them to a collection somewhere.

	return nil
}

func (c *Client) Close() {
	c.watch.Close()
}

func main() {
	client, err := NewClient(os.Args[1])
	fatalOnError(err)
	defer client.Close()

	client.SubscribeDir(".")

	go client.StartShell()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	func() {
		for {
			<-c

			bs, err := ioutil.ReadFile("./README.md")
			fatalOnError(err)

			r := bytes.NewReader(bs)
			err = client.Copy(r, "/tmp/README.md", "0755", int64(len(bs)))
			fatalOnError(err)

			fmt.Printf("Got Ctrl+C\n")
			os.Exit(1)
		}
	}()
}

func init() {
	flag.Parse()
}
