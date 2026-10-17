package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/report"
)

// export writes every violation of one check to the files run --csv and --jsonl
// name. Each file is written next to its target and renamed at the end, so a
// check that fails half way leaves no half of a list behind. The files are
// readable by their owner only: they hold the rows of the database.
type export struct {
	files []*exportFile
}

type exportFile struct {
	path  string
	tmp   *os.File
	write func(check.Sample) error
	flush func() error
}

func newExport(ch config.Check, csvPath, jsonlPath string) (*export, error) {
	e := &export{}
	for _, p := range []struct {
		path string
		csv  bool
	}{{csvPath, true}, {jsonlPath, false}} {
		if p.path == "" {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(p.path), ".money-checks-*")
		if err != nil {
			e.discard()
			return nil, err
		}
		f := &exportFile{path: p.path, tmp: tmp, flush: func() error { return nil }}
		if p.csv {
			w := report.NewCSV(tmp, check.Columns(ch), ch.HasAmount())
			f.write, f.flush = w.Write, w.Flush
		} else {
			f.write = report.NewJSONLines(tmp).Write
		}
		e.files = append(e.files, f)
	}
	return e, nil
}

// write is the export of check.RunExport.
func (e *export) write(s check.Sample) error {
	for _, f := range e.files {
		if err := f.write(s); err != nil {
			return fmt.Errorf("%s: %w", f.path, err)
		}
	}
	return nil
}

func (e *export) finish() error {
	var errs []error
	for _, f := range e.files {
		err := f.flush()
		if cerr := f.tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(f.tmp.Name(), f.path)
		}
		if err != nil {
			_ = os.Remove(f.tmp.Name())
			errs = append(errs, fmt.Errorf("%s: %w", f.path, err))
		}
	}
	return errors.Join(errs...)
}

func (e *export) discard() {
	for _, f := range e.files {
		_ = f.tmp.Close()
		_ = os.Remove(f.tmp.Name())
	}
}
