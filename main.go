package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/signal"
	"path"

	"github.com/rjeczalik/notify"
	"github.com/sabhiram/sshaddr"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
)

////////////////////////////////////////////////////////////////////////////////

var localDir string

////////////////////////////////////////////////////////////////////////////////

// Client wraps a `ssh.Client` which can monitor the file system for changes.
type Client struct {
	*ssh.Client // Client `is-a` *ssh.Client

	config *ssh.ClientConfig     // ssh connection config
	events chan notify.EventInfo // events channel for watched changes

	localDir  string // Local directory to keep in sync
	remoteDir string // Remote directory to push files to
}

// NewClient returns a ssh client which can watch files for changes.
func NewClient(addr, localDir string) (*Client, error) {
	ssha, err := sshaddr.Parse(addr)
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

	hostAddr := fmt.Sprintf("%s:%d", ssha.Host(), ssha.Port())
	client, err := ssh.Dial("tcp", hostAddr, config)
	if err != nil {
		return nil, err
	}

	return &Client{
		Client: client,

		config: config,
		events: make(chan notify.EventInfo, 1),

		localDir:  localDir,
		remoteDir: ssha.Destination(),
	}, nil
}

// StartShell creates a new ssh session and opens a shell to the remote address.
// It also hooks up the standard input / output pipes to allow terminal access
// which can be blocked by updates to subscribed files made in the local path.
func (c *Client) StartShell() error {
	// Subscribe to all changes in the local directory.
	c.SubscribeDir(c.localDir)

	// Create a new ssh session for use in a `shell`.
	sess, err := c.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	// Plumbing.
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	go io.Copy(os.Stdout, stdout) // session Stdout -> local Stdout
	go io.Copy(os.Stderr, stderr) // session Stderr -> local Stderr
	go io.Copy(stdin, os.Stdin)   // local Stdin -> session Stdin

	term_modes := ssh.TerminalModes{
		ssh.ECHO:  0,
		ssh.IGNCR: 1,
	}

	fd := int(os.Stdin.Fd())
	w, h, err := terminal.GetSize(fd)
	if err != nil {
		return err
	}

	if err := sess.RequestPty("xterm", h, w, term_modes); err != nil {
		return err
	}

	if err := sess.Shell(); err != nil {
		return err
	}

	for evt := range c.events {
		path := evt.Path()
		switch evt.Event() {
		case notify.Create:
			fmt.Printf("create :: %s\n", path)
		case notify.Remove:
			fmt.Printf("remove :: %s\n", path)
		case notify.Write:
			fmt.Printf("write  :: %s\n", path)
		case notify.Rename:
			fmt.Printf("rename :: %s\n", path)
		default:
			fmt.Printf("unknown (%d) :: %s", evt.Event(), path)
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

// SubscribeDir accepts a path to subscribe with the file watcher.  All events
// will be forwarded to the clients `events` channel.  If the `dirpath` ends
// with `/...` the watch will be recursive.
func (c *Client) SubscribeDir(dirpath string) error {
	return notify.Watch(dirpath, c.events, notify.All)
}

// Closes the `events` channel.
func (c *Client) Close() {
	close(c.events)
}

////////////////////////////////////////////////////////////////////////////////

func fatalOnError(err error) {
	if err != nil {
		fmt.Printf("Fatal error: %s\n", err.Error())
		os.Exit(1)
	}
}

func main() {
	client, err := NewClient(flag.Args()[0], localDir)
	fatalOnError(err)
	defer client.Close()

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
	flag.StringVar(&localDir, "local", "./...", "local directory to push to the remote")
	flag.Parse()
}
