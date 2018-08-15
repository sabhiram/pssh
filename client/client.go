package client

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rjeczalik/notify"
	"github.com/sabhiram/sshaddr"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
)

const isRecursiveWatch = true

// Client wraps a `ssh.Client` which can monitor the file system for changes.
type Client struct {
	*ssh.Client // Client `is-a` *ssh.Client

	config *ssh.ClientConfig     // ssh connection config
	events chan notify.EventInfo // events channel for watched changes

	localDir  string // Local directory to keep in sync
	remoteDir string // Remote directory to push files to
}

// New returns a ssh client which can watch files for changes.
func New(addr, localDir string) (*Client, error) {
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

	fmt.Printf("Connected!\n")

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
	dir := c.localDir
	if isRecursiveWatch {
		dir = path.Join(dir, "...")
	}
	c.SubscribeDir(dir)

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

	// Walk the local directory and recurse subdirs if the isRecursiveWalk is
	// set to true.
	files := []string{}
	if err := filepath.Walk(c.localDir, func(path string, f os.FileInfo, err error) error {
		// Ignore hidden files and directories.
		// TODO: Ignore files on the blacklist.
		if strings.HasPrefix(path, ".") {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return err
	}

	// Sync local files to remote
	for _, f := range files {
		dstPath := strings.TrimPrefix(f, filepath.Clean(c.localDir))
		if dstPath[0] == '/' {
			dstPath = dstPath[1:]
		}
		absLocal, err := filepath.Abs(f)
		if err != nil {
			absLocal = f
		}
		absDst := filepath.Join(c.remoteDir, dstPath)
		c.syncFiles(absLocal, absDst)
	}

	// Continue syncing any changes from here on out.
	for evt := range c.events {
		path := evt.Path()
		switch evt.Event() {
		case notify.Create:
			fmt.Printf("create :: %s\n", path)
			c.remoteCreateFile(path)
		case notify.Remove:
			fmt.Printf("remove :: %s\n", path)
			c.remoteRemoveFile(path)
		case notify.Write:
			fmt.Printf("write  :: %s\n", path)
			c.remoteUpdateFile(path)
		case notify.Rename:
			fmt.Printf("rename :: %s\n", path)
			c.remoteRenameFile(path)
		default:
			fmt.Printf("unknown (%d) :: %s", evt.Event(), path)
		}
	}
	return nil
}

// remoteRemoveFile is fired when the tracked file residing at `localPath` is
// removed.
func (c *Client) remoteRemoveFile(localPath string) error {
	return fmt.Errorf("remoteRemoveFile not implemented")
}

// remoteRenameFile is fired when the tracked file residing at `localPath` is
// renamed.
func (c *Client) remoteRenameFile(localPath string) error {
	return fmt.Errorf("remoteRenameFile not implemented")
}

////////////////////////////////////////////////////////////////////////////////

// Runs a `mkdir -p` for the given path to ensure that the other end has a
// valid directory at the specified `path`.
func (c *Client) ensureRemoteDirectory(path string) error {
	sess, err := c.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	cmd := fmt.Sprintf("mkdir -p %s", filepath.Dir(path))
	return sess.Run(cmd)
}

// copy creates a new session using the underlying ssh connection and copies
// the contents from the source reader into the destination path specified by
// `dstpath`.  The file's permissions and size are expected.
func (c *Client) copy(src io.Reader, dstpath, perms string, sz int64) error {
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

//Copies the contents of an os.File to a remote location, it will get the length of the file by looking it up from the filesystem
func (c *Client) copyFromFile(file os.File, remotePath string, perms string) error {
	stat, _ := file.Stat()
	return c.copy(&file, remotePath, perms, stat.Size())
}

// sync two files where both local and remote are absolute paths.
func (c *Client) syncFiles(local, remote string) error {
	f_local, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f_local.Close()

	fmt.Printf("Sync file: %s --> %s\n", local, remote)
	if err := c.ensureRemoteDirectory(remote); err != nil {
		return err
	}

	return c.copyFromFile(*f_local, remote, "0755")
}

// remoteUpdateFile is fired when the tracked file residing at `localPath` is
// updated.
func (c *Client) remoteUpdateFile(localPath string) error {
	localDir, err := filepath.Abs(c.localDir)
	if err != nil {
		return err
	}

	addedPath := strings.TrimPrefix(localPath, localDir)
	remotePath := filepath.Join(c.remoteDir, addedPath)
	return c.syncFiles(localPath, remotePath)
}

// remoteCreateFile is fired when the tracked file residing at `localPath` is
// created.
func (c *Client) remoteCreateFile(localPath string) error {
	return c.remoteUpdateFile(localPath)
}

////////////////////////////////////////////////////////////////////////////////

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
