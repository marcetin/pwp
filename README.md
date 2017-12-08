# Portable WP

This is a small proof of concept to create a command line utility for spinning up
portable WordPress sites that run on SQLite and using the built-in PHP server.

The goal is a severless WordPress environment for quick tests.

*Installing*

```
go get github.com/rheinardkorf/portable-wp/cmd/pwp
```

Make sure that $GOPATH/bin is in your paths. You can then run

```
pwp -help
```

to see what flags you can supply. Alternatively, simply run `pwp` without any
flags to use the convenient defaults.
