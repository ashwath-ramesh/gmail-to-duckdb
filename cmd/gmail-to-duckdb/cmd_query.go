package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
)

// runQuery parses the flags for a read command, reaches the database (through
// serve if it runs), and prints the envelope that do returns.
func runQuery(name string, args []string, stdout io.Writer, extra func(cmdFlags) func() error, do func(context.Context, query.Access, []string) (query.Envelope, error)) error {
	cf := globalFlags(name)
	asJSON := cf.fs.Bool("json", false, "JSON envelope")
	check := func() error { return nil }
	if extra != nil {
		check = extra(cf)
	}
	if err := cf.parse(args); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	return withAccess(*cf.db, func(a query.Access) error {
		env, err := do(context.Background(), a, cf.fs.Args())
		if err != nil {
			return err
		}
		return writeEnv(stdout, env, *asJSON)
	})
}

func cmdStatus(args []string, stdout io.Writer) error {
	return runQuery("status", args, stdout, nil, func(ctx context.Context, a query.Access, _ []string) (query.Envelope, error) {
		return a.Status(ctx)
	})
}

func cmdSchema(args []string, stdout io.Writer) error {
	return runQuery("schema", args, stdout, nil, func(ctx context.Context, a query.Access, _ []string) (query.Envelope, error) {
		return a.Schema(ctx)
	})
}

func cmdSearch(args []string, stdout io.Writer) error {
	return runQuery("search", args, stdout, nil, func(ctx context.Context, a query.Access, words []string) (query.Envelope, error) {
		q := strings.Join(words, " ")
		if strings.TrimSpace(q) == "" {
			return query.Envelope{}, fmt.Errorf("search requires a query")
		}
		return a.Search(ctx, q)
	})
}

func cmdGet(args []string, stdout io.Writer) error {
	var body *bool
	extra := func(cf cmdFlags) func() error {
		body = cf.fs.Bool("body", false, "include the stored email body (untrusted); this does not download it")
		return func() error {
			if cf.fs.NArg() != 1 {
				return fmt.Errorf("get requires one message id")
			}
			return nil
		}
	}
	return runQuery("get", args, stdout, extra, func(ctx context.Context, a query.Access, ids []string) (query.Envelope, error) {
		return a.Get(ctx, ids[0], *body)
	})
}

func cmdSQL(args []string, stdin io.Reader, stdout io.Writer) error {
	cf := globalFlags("sql")
	asJSON := cf.fs.Bool("json", false, "JSON envelope")
	write := cf.fs.Bool("write", false, "allow database DML/DDL (no files, attachments, or settings)")
	_ = cf.fs.Bool("read-only", false, "explicit read-only (default)")
	format := cf.fs.String("format", "", "json or table")
	if err := cf.parse(args); err != nil {
		return err
	}
	switch *format {
	case "":
	case "json":
		*asJSON = true
	case "table":
		*asJSON = false
	default:
		return fmt.Errorf("--format must be json or table, not %q", *format)
	}
	q := strings.Join(cf.fs.Args(), " ")
	if q == "" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		q = string(b)
	}
	if strings.TrimSpace(q) == "" {
		return fmt.Errorf("sql requires a query")
	}
	return withAccess(*cf.db, func(a query.Access) error {
		env, err := a.SQL(context.Background(), q, *write)
		if err != nil {
			return err
		}
		if *asJSON {
			return writeEnv(stdout, env, true)
		}
		return writeSQLTable(stdout, env.SQL)
	})
}
