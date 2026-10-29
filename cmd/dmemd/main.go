// Command dmemd starts the local memory service runtime.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	appruntime "github.com/N0rthHXD/dmemd/internal/runtime"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "dmemd:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("dmemd", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "JSON 配置文件路径（默认使用用户配置目录）")
	showVersion := flags.Bool("version", false, "输出版本后退出")
	checkConfig := flags.Bool("check-config", false, "加载并校验配置后退出")
	checkArtifacts := flags.Bool("artifact-integrity-check", false, "检查派生版本来源、内容和依赖一致性后退出")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Fprintf(stdout, "dmemd %s\n", version)
		return nil
	}

	loaded, err := appruntime.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	level, err := appruntime.ParseLogLevel(loaded.Config.Log.Level)
	if err != nil {
		return err
	}
	if *checkConfig {
		source := loaded.Path
		if source == "" {
			source = "built-in defaults"
		}
		fmt.Fprintf(stdout, "configuration valid: %s\n", source)
		return nil
	}

	if err := appruntime.Prepare(loaded.Config); err != nil {
		return err
	}

	logFile, err := appruntime.OpenLogFile(loaded.Config.Log.File)
	if err != nil {
		return fmt.Errorf("打开日志文件 %s: %w", loaded.Config.Log.File, err)
	}
	if logFile != nil {
		defer logFile.Close()
	}
	logOutput := stderr
	if logFile != nil {
		logOutput = io.MultiWriter(stderr, logFile)
	}
	logger := appruntime.NewLogger(logOutput, level)
	logger.Info("starting dmemd",
		"version", version,
		"config", loaded.Path,
		"data_dir", loaded.Config.DataDir,
		"listen", loaded.Config.Listen,
		"log_file", loaded.Config.Log.File,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lock, err := appruntime.AcquireInstance(loaded.Config.DataDir)
	if err != nil {
		return err
	}

	store, err := sqlite.Open(ctx, sqlite.Config{
		Path:               loaded.Config.Database.Path,
		BusyTimeout:        loaded.Config.Database.BusyTimeout.Std(),
		MaxReadConnections: loaded.Config.Database.MaxReadConnections,
	})
	if err != nil {
		_ = lock.Close()
		return fmt.Errorf("初始化数据库: %w", err)
	}
	if err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		_ = lock.Close()
		return fmt.Errorf("应用数据库迁移: %w", err)
	}
	if err := store.CheckPermissions(); err != nil {
		_ = store.Close()
		_ = lock.Close()
		return fmt.Errorf("检查数据库权限: %w", err)
	}
	logger.Info("database ready", "path", store.Path())
	if *checkArtifacts {
		defer lock.Close()
		defer store.Close()
		report, err := store.ArtifactIntegrityCheck(ctx)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return err
		}
		if len(report.Issues) > 0 {
			return fmt.Errorf("artifact integrity check: %d issues", len(report.Issues))
		}
		return nil
	}

	stack, err := appruntime.NewStack(loaded.Config, logger, store)
	if err != nil {
		_ = store.Close()
		_ = lock.Close()
		return err
	}
	if err := stack.Recover(ctx); err != nil {
		_ = store.Close()
		_ = lock.Close()
		return err
	}

	runErr := stack.App.Run(ctx)
	closeErr := store.Close()
	lockErr := lock.Close()
	if runErr != nil || closeErr != nil || lockErr != nil {
		return errors.Join(runErr, closeErr, lockErr)
	}
	return nil
}
