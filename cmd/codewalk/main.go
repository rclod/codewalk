// Command codewalk generates guided code walkthroughs.
//
// codewalk explains code so a human can understand it quickly: what a change
// does, how a system fits together, and where to look next. It explains code;
// it does not review or grade it.
package main

import (
	"os"

	"github.com/rclod/codewalk/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
