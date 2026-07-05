package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/rs/zerolog"
	"golang.org/x/term"
)

type pairConfig struct {
	storePath string
}

func parsePairArgs(args []string, stderr io.Writer) (pairConfig, error) {
	var cfg pairConfig
	defaultStore, err := defaultStorePath()
	if err != nil {
		return cfg, err
	}
	flags := flag.NewFlagSet("pair", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.storePath, "store", defaultStore, "linked-device SQLite store")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: meowcaller pair [flags]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return cfg, errors.Join(errUsage, err)
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return cfg, errUsage
	}
	return cfg, nil
}

func runPair(ctx context.Context, cfg pairConfig, stderr io.Writer) error {
	terminal, ok := stderr.(*os.File)
	if !ok || !term.IsTerminal(int(terminal.Fd())) {
		return errors.New("pair must run in an interactive terminal so the QR code stays out of service logs")
	}
	log := zerolog.Ctx(ctx)
	session, err := openSession(ctx, cfg.storePath, *log)
	if err != nil {
		return err
	}
	defer session.Close()
	if err := session.Pair(ctx, stderr); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "MeowCaller linked device ready")
	return nil
}
