package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/config"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/setup"
)

func cmdInit(args []string, stdout io.Writer) error {
	cf := globalFlags("init")
	if err := cf.fs.Parse(args); err != nil {
		return err
	}
	setup.PrintSteps(stdout)
	cred := *cf.creds
	if !flagSet(cf.fs, "credentials") {
		if _, err := os.Stat(cred); err != nil {
			cred = ""
		}
	}
	db := *cf.db
	if !flagSet(cf.fs, "db") {
		if _, err := os.Stat(config.Path()); err != nil {
			db = ""
		}
	}
	cfg, err := setup.Init(cred, db)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\n", config.Path())
	fmt.Fprintf(stdout, "credentials %s\n", cfg.Credentials)
	fmt.Fprintf(stdout, "db %s\n", cfg.DB)
	fmt.Fprintln(stdout, "next: gmail-to-duckdb doctor && gmail-to-duckdb serve --sync-every 5m")
	return nil
}

func cmdDoctor(args []string, stdout io.Writer) error {
	cf := globalFlags("doctor")
	asJSON := cf.fs.Bool("json", false, "JSON envelope")
	port := cf.fs.String("port", config.Load().Port, "UI port")
	if err := cf.fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Load()
	cfg.DB = *cf.db
	cfg.Credentials = *cf.creds
	cfg.OAuthPort = *cf.oauthPort
	cfg.Port = *port
	env := setup.Doctor(context.Background(), cfg)
	if err := writeEnv(stdout, env, *asJSON); err != nil {
		return err
	}
	if !setup.DoctorOK(env) {
		return fmt.Errorf("doctor found problems")
	}
	return nil
}

func flagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
