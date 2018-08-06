package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/crypto/ssh"
)

var (
	user string
	pass string
	addr string
	port int
)

func fatalOnError(err error) {
	if err != nil {
		fmt.Printf("Fatal error: %s\n", err.Error())
		os.Exit(1)
	}
}

func main() {
	/*
	 *  FS notification stuff here ...
	 */
	watcher, err := fsnotify.NewWatcher()
	fatalOnError(err)
	defer watcher.Close()

	done := make(chan struct{})
	go func() {
		count := 0
		for {
			select {
			case evt := <-watcher.Events:
				fmt.Printf("Got event %#v\n", evt)
				if evt.Op&fsnotify.Write == fsnotify.Write {
					fmt.Printf("  Wrote file %s\n", evt.Name)
				}
			case err := <-watcher.Errors:
				fmt.Printf("Got error: %s\n", err.Error())
				done <- struct{}{}
			}
			count += 1
			if count > 10 {
				done <- struct{}{}
			}
		}
	}()

	err = watcher.Add(".")
	fatalOnError(err)
	<-done

	/*
	 *  SSH stuff here ...
	 */
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(pass),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	conn, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", addr, port), config)
	fatalOnError(err)
	defer conn.Close()

	session, err := conn.NewSession()
	fatalOnError(err)
	defer session.Close()

	stdout, err := session.StdoutPipe()
	fatalOnError(err)
	go io.Copy(os.Stdout, stdout)

	stderr, err := session.StderrPipe()
	fatalOnError(err)
	go io.Copy(os.Stderr, stderr)

	stdin, err := session.StdinPipe()
	fatalOnError(err)
	go io.Copy(stdin, os.Stdin)

	term_modes := ssh.TerminalModes{
		ssh.ECHO:  0,
		ssh.IGNCR: 1,
	}

	err = session.RequestPty("xterm", 80, 40, term_modes)
	fatalOnError(err)

	err = session.Shell()
	fatalOnError(err)

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
	flag.StringVar(&user, "user", "", "username")
	flag.StringVar(&pass, "pass", "", "password")
	flag.StringVar(&addr, "addr", "", "host address")
	flag.IntVar(&port, "port", 22, "host port")
	flag.Parse()
}
