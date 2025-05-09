package fio

import (
	"encoding/json"
	"fmt"
	executorpkg "github.com/cirruslabs/tart/benchmark/internal/executor"
	"github.com/dustin/go-humanize"
	"github.com/gosuri/uitable"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapio"
	"os"
	"os/exec"
)

var debug bool
var image string
var prepare string

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fio",
		Short: "run Flexible I/O tester (fio) benchmarks",
		RunE:  run,
	}

	cmd.Flags().BoolVar(&debug, "debug", false, "enable debug logging")
	cmd.Flags().StringVar(&image, "image", "ghcr.io/cirruslabs/macos-sonoma-base:latest", "image to use for testing")
	cmd.Flags().StringVar(&prepare, "prepare", "", "command to run before running each benchmark")

	return cmd
}

func run(cmd *cobra.Command, args []string) error {
	config := zap.NewProductionConfig()
	if debug {
		config.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	}
	logger, err := config.Build()
	if err != nil {
		return err
	}
	defer func() {
		_ = logger.Sync()
	}()

	table := uitable.New()
	table.AddRow("Name", "Executor", "B/W (read)", "B/W (write)", "I/O (read)", "I/O (write)",
		"Latency (read)", "Latency (write)", "Latency (sync)")

	for _, benchmark := range benchmarks {
		for _, executorInitializer := range executorpkg.DefaultInitializers(cmd.Context(), image, logger) {
			if prepare != "" {
				shell := "/bin/sh"

				if shellFromEnv, ok := os.LookupEnv("SHELL"); ok {
					shell = shellFromEnv
				}

				logger.Sugar().Infof("running prepare command %q using shell %q",
					prepare, shell)

				cmd := exec.CommandContext(cmd.Context(), shell, "-c", prepare)

				loggerWriter := &zapio.Writer{Log: logger, Level: zap.DebugLevel}

				cmd.Stdout = loggerWriter
				cmd.Stderr = loggerWriter

				if err := cmd.Run(); err != nil {
					return fmt.Errorf("failed to run prepare command %q: %v", prepare, err)
				}
			}

			logger.Sugar().Infof("initializing executor %s", executorInitializer.Name)

			executor, err := executorInitializer.Fn()
			if err != nil {
				return err
			}

			logger.Sugar().Infof("installing Flexible I/O tester (fio) on executor %s",
				executorInitializer.Name)

			if _, err := executor.Run(cmd.Context(), "brew install fio"); err != nil {
				return err
			}

			logger.Sugar().Infof("running benchmark %q on %s executor", benchmark.Name,
				executorInitializer.Name)

			stdout, err := executor.Run(cmd.Context(), benchmark.Command)
			if err != nil {
				return err
			}

			var fioResult Result

			if err := json.Unmarshal(stdout, &fioResult); err != nil {
				return err
			}

			if len(fioResult.Jobs) != 1 {
				return fmt.Errorf("expected exactly 1 job from fio's JSON output, got %d",
					len(fioResult.Jobs))
			}

			job := fioResult.Jobs[0]

			readBandwidth := humanize.Bytes(uint64(job.Read.BW)*humanize.KByte) + "/s"
			readIOPS := humanize.SIWithDigits(job.Read.IOPS, 2, "IOPS")

			logger.Sugar().Infof("read bandwidth: %s, read IOPS: %s, read latency: %s",
				readBandwidth, readIOPS, job.Read.LatencyNS.String())

			writeBandwidth := humanize.Bytes(uint64(job.Write.BW)*humanize.KByte) + "/s"
			writeIOPS := humanize.SIWithDigits(job.Write.IOPS, 2, "IOPS")

			logger.Sugar().Infof("write bandwidth: %s, write IOPS: %s, write latency: %s",
				writeBandwidth, writeIOPS, job.Write.LatencyNS.String())

			logger.Sugar().Infof("sync latency: %s", job.Sync.LatencyNS.String())

			table.AddRow(benchmark.Name, executorInitializer.Name, readBandwidth, writeBandwidth,
				readIOPS, writeIOPS, job.Read.LatencyNS.String(), job.Write.LatencyNS.String(),
				job.Sync.LatencyNS.String())

			if err := executor.Close(); err != nil {
				return fmt.Errorf("failed to close executor %s: %w",
					executorInitializer.Name, err)
			}
		}
	}

	fmt.Println(table.String())

	return nil
}
