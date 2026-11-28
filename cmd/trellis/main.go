// Command trellis is one judge over an estate's standards. serve runs the
// board on a schedule; once prints one pass; check judges a repository in
// its pipeline with the same packs.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/orchard-suite/trellis/internal/sources/telemetry/declaration"
)

// version is set at build time from VERSION.
var version = "dev"

const usage = `trellis: one judge over an estate's standards.

  trellis serve [-config trellis.config.yaml]
        the board: every pack the config switches on, judged on a schedule
  trellis once  [-config trellis.config.yaml] [-packs-dir drafts/]
        one pass of the board, printed; -packs-dir tries packs on the real
        estate before they are switched on
  trellis check [-dir .] [-packs interfaces,build] [-packs-dir dir]
                [-base ref|none] [-match 'app-v*'] [-fail-on high] [-json]
                [-declaration trellis.yaml -prometheus URL -loki URL -tempo URL]
        judges a repository checkout with the same packs: its tree, the
        APIs its orrery.yaml offers with what changed since the latest
        release tag, and, given a declaration and the stores, its telemetry.
        Exits 1 when a finding is at least as bad as -fail-on.
  trellis refresh [-control 127.0.0.1:8082] [-timeout 20m]
        asks the running board for a pass now, on its control port, and
        waits for it and its sends; exits 1 when a source or a sink failed.
        In a cluster: kubectl exec deploy/trellis -- /app/trellis refresh
  trellis judge [-control 127.0.0.1:8082] [-timeout 1m] [statement.json]
        hands one change statement, from the file or stdin, to the running
        board's judge on its control port and prints the answer: the route,
        its reasons, and each draft's. Exits 1 when the statement is refused.
  trellis replay [-config file] -pack id [-packs-dir drafts/] [-since 30d]
                 [-until time] [-catalog=false] [-json]
        judges a gate pack over the past changes Annals keeps, joined to what
        each did, and prints how many it would have asked of people and how
        many it would have passed that the watch saw do harm. Exits 1 on a miss.
  trellis test  [-packs-dir dir] [-fixtures file]
        holds an estate's packs to their fixtures: a failing, a passing,
        and a no-data case for every rule
  trellis schema    the JSON Schema of a telemetry declaration
  trellis version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "once":
		err = runOnce(os.Args[2:])
	case "check":
		err = runCheck(os.Args[2:])
	case "refresh":
		err = runRefresh(os.Args[2:])
	case "judge":
		err = runJudge(os.Args[2:])
	case "replay":
		err = runReplay(os.Args[2:])
	case "test":
		err = runTest(os.Args[2:])
	case "schema":
		_, err = os.Stdout.Write(declaration.Schema)
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "trellis: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	var fail *failure
	switch {
	case errors.As(err, &fail):
		fmt.Fprintln(os.Stderr, "trellis:", fail)
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "trellis:", err)
		os.Exit(2)
	}
}

// failure is a judgement that ran and found something at or over the bar,
// as opposed to an error running it.
type failure struct {
	n     int
	floor string
}

func (f *failure) Error() string {
	switch f.floor {
	case "fixture":
		return fmt.Sprintf("%d fixture problems", f.n)
	case "pass":
		return fmt.Sprintf("%d sources or sinks failed in the pass", f.n)
	case "statement":
		return "the judge refused the statement"
	case "miss":
		return fmt.Sprintf("the pack would have passed %d changes the watch saw do harm", f.n)
	}
	return fmt.Sprintf("%d findings at %s or worse", f.n, f.floor)
}
