package main

import (
	"fmt"
	"os"

	"github.com/sanspareilsmyn/kforward/internal/cli"

	"go.uber.org/zap"
)

func main() {
	config := zap.NewDevelopmentConfig()
	config.Level = zap.NewAtomicLevelAt(zap.InfoLevel)

	logger, err := config.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize zap logger: %v\n", err)
		os.Exit(1)
	}

	defer func(logger *zap.Logger) {
		err := logger.Sync()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to sync zap logger: %v\n", err)
		}
	}(logger)

	zap.ReplaceGlobals(logger)
	zap.S().Infow("kforward starting", "version", "dev")

	rootCmd := cli.NewRootCmd()

	if err := rootCmd.Execute(); err != nil {
		zap.S().Errorw("Command execution failed", "error", err)
		os.Exit(1)
	}

	zap.S().Infow("kforward finished successfully")
}
