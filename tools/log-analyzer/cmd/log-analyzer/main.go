package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cursor-log-analyzer/internal/project"
)

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("path cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "log-analyzer: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("log-analyzer", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var inputs stringList
	var baselines stringList
	var output string
	var allowUnknown bool
	flags.Var(&inputs, "input", "日志目录或 events.jsonl，可重复")
	flags.Var(&baselines, "baseline", "可选对比基线目录，可重复")
	flags.StringVar(&output, "out", "", "报告输出目录（必填）")
	flags.BoolVar(&allowUnknown, "allow-unknown-schema", false, "兼容读取未知 schema，并输出警告")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(flags.Output(), "用法: log-analyzer -input <日志目录> [-input <relay日志>] -out <输出目录>")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if len(inputs) == 0 {
		return errors.New("至少需要一个 -input")
	}
	if strings.TrimSpace(output) == "" {
		return errors.New("必须指定 -out")
	}
	if err := validateOutputIsolation(output, append(append([]string(nil), inputs...), baselines...)); err != nil {
		return err
	}

	ctx := context.Background()
	analysis, err := project.Open(ctx, project.OpenRequest{
		Inputs:             inputs,
		Baselines:          baselines,
		AllowUnknownSchema: allowUnknown,
	})
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = analysis.Close()
		}
	}()

	staged, err := analysis.StageReport(ctx, output)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = staged.Cleanup()
		}
	}()
	if err := analysis.Close(); err != nil {
		return err
	}
	closed = true
	if err := staged.Publish(); err != nil {
		return err
	}
	published = true
	summary := analysis.Summary()
	absoluteOutput, _ := filepath.Abs(output)
	_, _ = fmt.Fprintf(os.Stdout, "分析完成: events=%d traces=%d findings=%d output=%s\n", summary.EventCount, summary.TraceCount, summary.FindingCount, absoluteOutput)
	return nil
}

func validateOutputIsolation(output string, inputs []string) error {
	absoluteOutput, err := filepath.Abs(strings.TrimSpace(output))
	if err != nil {
		return err
	}
	for _, input := range inputs {
		absoluteInput, err := filepath.Abs(strings.TrimSpace(input))
		if err != nil {
			return err
		}
		info, statErr := os.Stat(absoluteInput)
		if statErr == nil && !info.IsDir() {
			absoluteInput = filepath.Dir(absoluteInput)
		}
		if sameOrWithin(absoluteOutput, absoluteInput) {
			return fmt.Errorf("输出目录不能位于输入目录内: %s", absoluteInput)
		}
	}
	return nil
}

func sameOrWithin(path string, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
