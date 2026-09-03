package cli

import (
	"context"
	"fmt"
)

const evalUsage = `codewalk eval — evaluate walkthrough quality

Usage:
  codewalk eval check <run-id|walkthrough.json>   Deterministic checks only
  codewalk eval run <run-id|walkthrough.json>     Deterministic and semantic evaluation
  codewalk eval benchmark <case-dir>              Generate and evaluate a benchmark case
  codewalk eval suite <corpus-dir>                Run a whole benchmark corpus
  codewalk eval compare <run-a> <run-b>           Blind pairwise comparison
  codewalk eval report <run-id>                   Print a readable quality report
`

func runEval(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(env.Stdout, evalUsage)
		return nil
	}
	switch args[0] {
	case "check":
		return evalCheck(ctx, env, args[1:])
	case "run":
		return evalRun(ctx, env, args[1:])
	case "benchmark":
		return evalBenchmark(ctx, env, args[1:])
	case "suite":
		return evalSuite(ctx, env, args[1:])
	case "compare":
		return evalCompare(ctx, env, args[1:])
	case "report":
		return evalReport(ctx, env, args[1:])
	case "-h", "--help":
		fmt.Fprint(env.Stdout, evalUsage)
		return nil
	default:
		return fmt.Errorf("unknown eval subcommand %q", args[0])
	}
}
