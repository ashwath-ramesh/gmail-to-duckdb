package main

import (
	"context"
	"fmt"
	"io"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
)

func cmdStatus(args []string, stdout io.Writer) error {
	cf := globalFlags("status")
	asJSON := cf.fs.Bool("json", false, "JSON envelope")
	if err := cf.fs.Parse(args); err != nil {
		return err
	}
	return withAccess(*cf.db, func(a query.Access) error {
		env, err := a.Status(context.Background())
		if err != nil {
			return err
		}
		return writeEnv(stdout, env, *asJSON)
	})
}

func cmdSearch(args []string, stdout io.Writer) error {
	cf := globalFlags("search")
	asJSON := cf.fs.Bool("json", false, "JSON envelope")
	if err := cf.fs.Parse(args); err != nil {
		return err
	}
	q := cf.fs.Arg(0)
	if q == "" {
		return fmt.Errorf("search requires a query")
	}
	return withAccess(*cf.db, func(a query.Access) error {
		env, err := a.Search(context.Background(), q)
		if err != nil {
			return err
		}
		return writeEnv(stdout, env, *asJSON)
	})
}

func cmdGet(args []string, stdout io.Writer) error {
	cf := globalFlags("get")
	asJSON := cf.fs.Bool("json", false, "JSON envelope")
	body := cf.fs.Bool("body", false, "include untrusted email body")
	if err := cf.fs.Parse(args); err != nil {
		return err
	}
	id := cf.fs.Arg(0)
	if id == "" {
		return fmt.Errorf("get requires a message id")
	}
	return withAccess(*cf.db, func(a query.Access) error {
		env, err := a.Get(context.Background(), id, *body)
		if err != nil {
			return err
		}
		return writeEnv(stdout, env, *asJSON)
	})
}

func cmdSchema(args []string, stdout io.Writer) error {
	cf := globalFlags("schema")
	asJSON := cf.fs.Bool("json", false, "JSON envelope")
	if err := cf.fs.Parse(args); err != nil {
		return err
	}
	return withAccess(*cf.db, func(a query.Access) error {
		env, err := a.Schema(context.Background())
		if err != nil {
			return err
		}
		return writeEnv(stdout, env, *asJSON)
	})
}

func cmdSQL(args []string, stdin io.Reader, stdout io.Writer) error {
	cf := globalFlags("sql")
	asJSON := cf.fs.Bool("json", false, "JSON envelope")
	write := cf.fs.Bool("write", false, "allow mutating SQL")
	_ = cf.fs.Bool("read-only", false, "explicit read-only (default)")
	format := cf.fs.String("format", "", "json or table")
	if err := cf.fs.Parse(args); err != nil {
		return err
	}
	if *format == "json" {
		*asJSON = true
	}
	q := cf.fs.Arg(0)
	if q == "" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		q = string(b)
	}
	if q == "" {
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
