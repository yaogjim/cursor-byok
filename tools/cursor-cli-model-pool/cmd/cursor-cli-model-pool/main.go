package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"cursor-cli-model-pool/internal/engine"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cursor-cli-model-pool <validate|dry-run|run>")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctrl := &engine.Engine{}
	switch args[0] {
	case "validate":
		if err := ctrl.Validate(ctx); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("ok")
		return 0
	case "dry-run":
		plan, err := ctrl.DryRun(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, model := range plan.Models {
			fmt.Println(strings.Join(model.Argv, " "))
		}
		return 0
	case "run":
		prompt, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取 stdin prompt 失败")
			return 1
		}
		out, err := ctrl.Run(ctx, prompt)
		if err != nil && !out.Success {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: cursor-cli-model-pool <validate|dry-run|run>")
		return 2
	}
}
