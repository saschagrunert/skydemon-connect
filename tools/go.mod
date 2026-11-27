// Development tools, kept in a module of their own so that the main module
// stays free of dependencies and Dependabot keeps the versions current.
module github.com/saschagrunert/skydemon-connect/tools

go 1.27

toolchain go1.27.4

tool (
	github.com/tc-hib/go-winres
	golang.org/x/vuln/cmd/govulncheck
)

require (
	github.com/cpuguy83/go-md2man/v2 v2.0.7 // indirect
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/tc-hib/go-winres v0.3.3 // indirect
	github.com/tc-hib/winres v0.3.1 // indirect
	github.com/urfave/cli/v2 v2.27.7 // indirect
	github.com/xrash/smetrics v0.0.0-20240521201337-686a1a2994c1 // indirect
	golang.org/x/image v0.48.0 // indirect
	golang.org/x/mod v0.43.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/telemetry v0.0.0-20261116191925-e77a43154acb // indirect
	golang.org/x/tools v0.53.0 // indirect
	golang.org/x/vuln v1.10.0 // indirect
)
