# pSSH

Push files + SSH in golang.

## Why?

Why not? J/K.

I wanted a simple way to keep a directory in sync with a remote counterpart while maintaining a remote ssh connection to a server that I was poking around in.

## Install

```
go get github.com/sabhiram/pssh
```

## Usage

Sync the current working directory to `/tmp/foobar` and open a shell to `foobar.com:2222`:
```
pssh -local . user@foobar.com:2222:/tmp/foobar
```
