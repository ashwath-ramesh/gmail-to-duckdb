package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/config"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/setup"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/termtext"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func cmdInit(args []string, stdout io.Writer) error {
	cf := globalFlags("init")
	if err := parseFlags(cf.fs, args); err != nil {
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
	fmt.Fprintf(stdout, "wrote %s\n", termtext.SingleLine(config.Path()))
	fmt.Fprintf(stdout, "credentials %s\n", termtext.SingleLine(cfg.Credentials))
	fmt.Fprintf(stdout, "db %s\n", termtext.SingleLine(cfg.DB))
	fmt.Fprintln(stdout, "next: gmail-to-duckdb serve --sync-every 5m")
	return nil
}

// cmdLogout revokes Google access for one database and removes its token
// file. It does not touch the mail copy.
func cmdLogout(args []string, stdout io.Writer) error {
	cf := globalFlags("logout")
	if err := cf.parse(args); err != nil {
		return err
	}
	if _, err := web.Dial(*cf.db); err == nil {
		// serve keeps the token in memory and writes it back on refresh.
		return fmt.Errorf("serve is running for this database; stop it, then run logout again")
	}
	ctx, stop := signalContext()
	defer stop()
	tokPath := auth.TokenPath(*cf.db)
	revoked, err := auth.Logout(ctx, tokPath)
	switch {
	case errors.Is(err, auth.ErrNotSignedIn):
		fmt.Fprintf(stdout, "not signed in (no %s)\n", termtext.SingleLine(tokPath))
		return nil
	case err != nil:
		return fmt.Errorf("%w; the token file is kept, so try again, or remove access at https://myaccount.google.com/permissions", err)
	case revoked:
		fmt.Fprintln(stdout, "Google access is revoked")
	default:
		fmt.Fprintln(stdout, "Google access had already ended")
	}
	fmt.Fprintf(stdout, "removed %s; the mail copy is not changed\n", termtext.SingleLine(tokPath))
	return nil
}

func cmdDoctor(args []string, stdout io.Writer) error {
	cf := globalFlags("doctor")
	asJSON := cf.fs.Bool("json", false, "JSON envelope")
	port := cf.fs.String("port", cf.cfg.Port, "UI port")
	if err := cf.parse(args); err != nil {
		return err
	}
	cfg := cf.cfg
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
