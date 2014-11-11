package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/pprof"

	logging "github.com/jbenet/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-logging"
	ma "github.com/jbenet/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-multiaddr"
	manet "github.com/jbenet/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-multiaddr/net"

	cmds "github.com/jbenet/go-ipfs/commands"
	cmdsCli "github.com/jbenet/go-ipfs/commands/cli"
	cmdsHttp "github.com/jbenet/go-ipfs/commands/http"
	"github.com/jbenet/go-ipfs/config"
	"github.com/jbenet/go-ipfs/core"
	commands "github.com/jbenet/go-ipfs/core/commands2"
	daemon "github.com/jbenet/go-ipfs/daemon2"
	u "github.com/jbenet/go-ipfs/util"
)

// log is the command logger
var log = u.Logger("cmd/ipfs")

const (
	heapProfile = "ipfs.mprof"
	errorFormat = "ERROR: %v\n\n"
)

var ofi io.WriteCloser

func main() {
	handleInterrupt()

	args := os.Args[1:]
	req, root, err := createRequest(args)
	if err != nil {
		fmt.Println(err)
		exit(1)
	}
	handleOptions(req, root)

	// if debugging, setup profiling.
	if u.Debug {
		var err error
		ofi, err = os.Create("cpu.prof")
		if err != nil {
			fmt.Println(err)
			return
		}

		pprof.StartCPUProfile(ofi)
	}

	res := callCommand(req, root)
	outputResponse(res, root)

	exit(0)
}

func createRequest(args []string) (cmds.Request, *cmds.Command, error) {
	req, root, cmd, path, err := cmdsCli.Parse(args, Root, commands.Root)

	// handle parse error (which means the commandline input was wrong,
	// e.g. incorrect number of args, or nonexistent subcommand)
	if err != nil {
		// if the -help flag wasn't specified, show the error message
		// or if a path was returned (user specified a valid subcommand), show the error message
		// (this means there was an option or argument error)
		if path != nil && len(path) > 0 {
			help, _ := req.Option("help").Bool()
			if !help {
				fmt.Printf(errorFormat, err)
			}
		}

		// when generating help for the root command, we don't want the autogenerated subcommand text
		// (since we have better hand-made subcommand list in the root Help field)
		if cmd == nil {
			root = &*commands.Root
			root.Subcommands = nil
		}

		// generate the help text for the command the user was trying to call (or root)
		helpText, htErr := cmdsCli.HelpText("ipfs", root, path)
		if htErr != nil {
			fmt.Println(htErr)
		} else {
			fmt.Println(helpText)
		}
		return nil, nil, err
	}

	configPath, err := getConfigRoot(req)
	if err != nil {
		return nil, nil, err
	}

	conf, err := getConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	ctx := req.Context()
	ctx.ConfigRoot = configPath
	ctx.Config = conf

	if !req.Option("encoding").Found() {
		if req.Command().Marshallers != nil && req.Command().Marshallers[cmds.Text] != nil {
			req.SetOption("encoding", cmds.Text)
		} else {
			req.SetOption("encoding", cmds.JSON)
		}
	}

	return req, root, nil
}

func handleOptions(req cmds.Request, root *cmds.Command) {
	help, err := req.Option("help").Bool()
	if help && err == nil {
		helpText, err := cmdsCli.HelpText("ipfs", root, req.Path())
		if err != nil {
			fmt.Println(err.Error())
		} else {
			fmt.Println(helpText)
		}
		exit(0)
	} else if err != nil {
		fmt.Println(err)
		exit(1)
	}

	if debug, err := req.Option("debug").Bool(); debug && err == nil {
		u.Debug = true
		u.SetAllLoggers(logging.DEBUG)
	} else if err != nil {
		fmt.Println(err)
		exit(1)
	}
}

func callCommand(req cmds.Request, root *cmds.Command) cmds.Response {
	var res cmds.Response

	if root == Root {
		res = root.Call(req)

	} else {
		local, err := req.Option("local").Bool()
		if err != nil {
			fmt.Println(err)
			exit(1)
		}

		if (!req.Option("local").Found() || !local) && daemon.Locked(req.Context().ConfigRoot) {
			addr, err := ma.NewMultiaddr(req.Context().Config.Addresses.API)
			if err != nil {
				fmt.Println(err)
				exit(1)
			}

			_, host, err := manet.DialArgs(addr)
			if err != nil {
				fmt.Println(err)
				exit(1)
			}

			client := cmdsHttp.NewClient(host)

			res, err = client.Send(req)
			if err != nil {
				fmt.Println(err)
				exit(1)
			}

		} else {
			node, err := core.NewIpfsNode(req.Context().Config, false)
			if err != nil {
				fmt.Println(err)
				exit(1)
			}
			defer node.Close()
			req.Context().Node = node

			res = root.Call(req)
		}
	}

	return res
}

func outputResponse(res cmds.Response, root *cmds.Command) {
	if res.Error() != nil {
		fmt.Printf(errorFormat, res.Error().Error())

		if res.Error().Code == cmds.ErrClient {
			helpText, err := cmdsCli.HelpText("ipfs", root, res.Request().Path())
			if err != nil {
				fmt.Println(err.Error())
			} else {
				fmt.Println(helpText)
			}
		}

		exit(1)
	}

	out, err := res.Reader()
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	io.Copy(os.Stdout, out)
}

func getConfigRoot(req cmds.Request) (string, error) {
	configOpt, err := req.Option("config").String()
	if err != nil {
		return "", err
	}
	if configOpt != "" {
		return configOpt, nil
	}

	configPath, err := config.PathRoot()
	if err != nil {
		return "", err
	}
	return configPath, nil
}

func getConfig(path string) (*config.Config, error) {
	configFile, err := config.Filename(path)
	if err != nil {
		return nil, err
	}

	return config.Load(configFile)
}

func writeHeapProfileToFile() error {
	mprof, err := os.Create(heapProfile)
	if err != nil {
		log.Fatal(err)
	}
	defer mprof.Close()
	return pprof.WriteHeapProfile(mprof)
}

// listen for and handle SIGTERM
func handleInterrupt() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	go func() {
		for _ = range c {
			log.Info("Received interrupt signal, terminating...")
			exit(0)
		}
	}()
}

func exit(code int) {
	if u.Debug {
		pprof.StopCPUProfile()
		ofi.Close()

		err := writeHeapProfileToFile()
		if err != nil {
			log.Critical(err)
		}
	}

	os.Exit(code)
}
