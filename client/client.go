package client

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rjeczalik/notify"
	"github.com/sabhiram/sshaddr"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
)

////////////////////////////////////////////////////////////////////////////////

// checkForUserCertAuth returns any valid `ssh.AuthMethod`s available for the
// specified user.  Permission errors should be treated correctly to allow
// correct execution.  It is valid for this function to return nil, nil to
// signal that nothing major went wrong but that we found no valid certs.
func checkForUserCertAuth(username string) ([]ssh.AuthMethod, error) {
	ret := []ssh.AuthMethod{}

	u, err := user.Lookup(username)
	if err != nil {
		return nil, err
	}

	base := path.Join(u.HomeDir, ".ssh")
	for _, k := range []string{"id_rsa", "id_dsa"} {
		pkf := path.Join(base, k)
		fmt.Printf("PKF=%s\n", pkf)
		if _, err := os.Stat(pkf); err == nil {
			bs, err := ioutil.ReadFile(pkf)
			if err != nil {
				return nil, err
			}

			k, err := ssh.ParsePrivateKey(bs)
			if err != nil {
				return nil, err
			}

			// TODO: Handle if there is a passphrase.
			// https: //github.com/golang/crypto/blob/master/ssh/agent/keyring.go

			ret = append(ret, ssh.PublicKeys(k))
		}
	}
	return ret, nil
}

////////////////////////////////////////////////////////////////////////////////

const isRecursiveWatch = true

////////////////////////////////////////////////////////////////////////////////

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

	host, port := ssha.Host(), ssha.Port()
	user, pass, auth := ssha.User(), ssha.Pass(), []ssh.AuthMethod{}

	if len(pass) == 0 {
		// No pass specified - check for cert based auth.
		cert_auths, err := checkForUserCertAuth(user)
		if err != nil {
			return nil, err
		} else if len(cert_auths) > 0 {
			auth = append(auth, cert_auths...)
		}

		// Password not specified and the key files are missing, prompt
		// the shell for a password.
		if len(auth) == 0 {
			fmt.Printf("%s@%s's password: ", user, host)
			bs, err := terminal.ReadPassword(int(syscall.Stdin))
			if err != nil {
				return nil, err
			}
			fmt.Printf("\n")
			auth = append(auth, ssh.Password(string(bs)))
		}
	} else {
		auth = append(auth, ssh.Password(pass))
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
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

// Attempt to update status on the same status line  ... wip
func (c *Client) status(msg string) error {
	// fmt.Printf("\033[A\033[2K\r")
	fmt.Printf("\r%s\n", msg)
	// fmt.Printf(msg + "\n")
	return nil
}

func setupTerminalForSession(fd int, sess *ssh.Session) (*terminal.State, error) {
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	termState, err := terminal.MakeRaw(fd)
	if err != nil {
		return nil, err
	}

	w, h, err := terminal.GetSize(fd)
	if err != nil {
		return nil, err
	}

	return termState, sess.RequestPty("xterm-256color", h, w, modes)
}

func restoreTerminal(fd int, state *terminal.State) error {
	return terminal.Restore(fd, state)
}

// StartShell creates a new ssh session and opens a shell to the remote address.
// It also hooks up the standard input / output pipes to allow terminal access
// which can be blocked by updates to subscribed files made in the local path.
func (c *Client) StartShell(skipInitialSync bool) error {
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
	sessStdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	sessStderr, err := sess.StderrPipe()
	if err != nil {
		return err
	}
	sessStdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}

	localStdin := os.Stdin
	localStdout, localStderr := os.Stdout, os.Stderr
	go io.Copy(localStdout, sessStdout) // session Stdout -> local Stdout
	go io.Copy(localStderr, sessStderr) // session Stderr -> local Stderr
	go io.Copy(sessStdin, localStdin)   // local Stdin -> session Stdin

	/*
	 *  Setup the terminal in raw mode and request the appropriate h x w.
	 */
	fd := int(localStdin.Fd())
	oldState, err := setupTerminalForSession(fd, sess)
	if err != nil {
		return err
	}
	defer restoreTerminal(fd, oldState)

	if err := sess.Shell(); err != nil {
		return err
	}

	// Walk the local directory and recurse subdirs if the isRecursiveWalk is
	// set to true.  Only do this if the `skipInitialSync` is not set.
	if !skipInitialSync {
		files := []string{}
		if err := filepath.Walk(c.localDir, func(path string, f os.FileInfo, err error) error {
			// Ignore hidden files and directories.
			// TODO: Ignore files on the blacklist.
			if strings.HasPrefix(path, ".") || f.IsDir() {
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
			c.syncLocalFileToRemote(absLocal, absDst)
		}
	}

	// TODO: We need a way to break out of this :)
	// Continue syncing any changes from here on out.
	for evt := range c.events {
		path := evt.Path()
		switch evt.Event() {
		case notify.Create:
			c.status(fmt.Sprintf("create :: %s", path))
			c.remoteCreateFile(path)
		case notify.Remove:
			c.status(fmt.Sprintf("remove :: %s", path))
			c.remoteRemoveFile(path)
		case notify.Write:
			c.status(fmt.Sprintf("write  :: %s", path))
			c.remoteUpdateFile(path)
		case notify.Rename:
			c.status(fmt.Sprintf("rename :: %s", path))
			c.remoteRenameFile(path)
		default:
			c.status(fmt.Sprintf("unknown (%d) :: %s", evt.Event(), path))
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

// runRemoteCommand runs
func (c *Client) runRemoteCommand(cmd string) error {
	sess, err := c.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	return sess.Run(cmd)
}

// Runs a `mkdir -p` for the given path to ensure that the other end has a
// valid directory at the specified `path`.
func (c *Client) ensureRemoteDirectory(path string) error {
	cmd := fmt.Sprintf("mkdir -p %s", filepath.Dir(path))
	return c.runRemoteCommand(cmd)
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
func (c *Client) syncLocalFileToRemote(local, remote string) error {
	f_local, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f_local.Close()

	status := fmt.Sprintf("Sync file: %s --> %s", local, remote)
	c.status(status)
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
	return c.syncLocalFileToRemote(localPath, remotePath)
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

// Close closes the `events` channel.
func (c *Client) Close() {
	close(c.events)
}
