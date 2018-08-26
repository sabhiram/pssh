package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/sabhiram/pssh/client"
)

////////////////////////////////////////////////////////////////////////////////

var (
	localDir        string
	skipInitialSync bool
)

func fatalOnError(err error) {
	if err != nil {
		fmt.Printf("Fatal error: %s\n", err.Error())
		os.Exit(1)
	}
}

func main() {
	connAddr := flag.Args()[0]
	client, err := client.New(connAddr, localDir)
	fatalOnError(err)
	defer client.Close()

	go client.StartShell(skipInitialSync)

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
	flag.StringVar(&localDir, "local", "./", "local directory to push to the remote")
	flag.BoolVar(&skipInitialSync, "skip-sync", false, "if true, will skip the initial sync")
	flag.Parse()
}

/*
TODO:
	folder creation does not work :) - it makes a remote file instead?
*/
