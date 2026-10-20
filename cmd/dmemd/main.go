// Command dmemd starts the local memory service runtime.
package main

import (
	"context"
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

	logger := appruntime.NewLogger(stderr, level)
	logger.Info("starting dmemd",
		"version", version,
		"config", loaded.Path,
		"data_dir", loaded.Config.DataDir,
		"listen", loaded.Config.Listen,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := sqlite.Open(ctx, sqlite.Config{
		Path:               loaded.Config.Database.Path,
		BusyTimeout:        loaded.Config.Database.BusyTimeout.Std(),
		MaxReadConnections: loaded.Config.Database.MaxReadConnections,
	})
	if err != nil {
		return fmt.Errorf("初始化数据库: %w", err)
	}
	if err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		return fmt.Errorf("应用数据库迁移: %w", err)
	}
	if err := store.CheckPermissions(); err != nil {
		_ = store.Close()
		return fmt.Errorf("检查数据库权限: %w", err)
	}
	logger.Info("database ready", "path", store.Path())

	app, err := appruntime.New(loaded.Config, logger, store)
	if err != nil {
		_ = store.Close()
		return err
	}

	runErr := app.Run(ctx)
	closeErr := store.Close()
	if runErr != nil || closeErr != nil {
		return errors.Join(runErr, closeErr)
	}
	return nil
}
