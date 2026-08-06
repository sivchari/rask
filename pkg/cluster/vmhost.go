package cluster

import (
	"flag"
	"fmt"
	"io"
)

// vmHostArg is the hidden entrypoint token internal/substrate/vz's
// Runtime.spawnVMHost execs this binary with (as argv[1]) to host one
// cluster's VM for its entire lifetime, outliving the "rask create"
// invocation that spawned it — see that package's doc comment for why the
// VM has to live in a separate, detached process at all. Matching it here,
// directly against os.Args, is what lets RunVMHostIfRequested work before
// any of a consuming program's own flag/subcommand parsing runs, with no
// cobra (or any other CLI framework) dependency imposed on that program.
const vmHostArg = "__vm-host"

// parseVMHostArgs reports whether args (os.Args) requests the vm-host
// entrypoint — args[1] == vmHostArg, matching how a subcommand dispatcher
// like cobra itself would match the first positional argument. If not, ok
// is false and home/name/err are all zero; the caller must not treat that
// as a failure, only as "not our concern".
//
// If args does request it, ok is true and the remaining arguments
// (args[2:]) are parsed for the same --home/--name flags spawnVMHost
// passes (internal/substrate/vz/vz.go); err reports a missing or malformed
// flag. Once ok is true, handling the request (successfully or not) is
// this process's responsibility for the rest of its life — there is no
// sensible fallback path once the token has matched.
func parseVMHostArgs(args []string) (home, name string, ok bool, err error) {
	if len(args) < 2 || args[1] != vmHostArg {
		return "", "", false, nil
	}

	fs := flag.NewFlagSet(vmHostArg, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&home, "home", "", "rask home directory")
	fs.StringVar(&name, "name", "", "cluster name")

	if parseErr := fs.Parse(args[2:]); parseErr != nil {
		return "", "", true, fmt.Errorf("cluster: parsing %s arguments: %w", vmHostArg, parseErr)
	}

	if home == "" {
		return "", "", true, fmt.Errorf("cluster: %s requires --home", vmHostArg)
	}

	if name == "" {
		return "", "", true, fmt.Errorf("cluster: %s requires --name", vmHostArg)
	}

	return home, name, true, nil
}
