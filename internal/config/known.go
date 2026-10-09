package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ianfoxdev/money-checks/internal/money"
)

// Known is a violation someone has looked at and accepted, with the reason. It is
// still reported, but it does not count as a violation of its check.
type Known struct {
	Check  string
	ID     string
	Reason string
	// Amount, when set, must match the violation; a different amount is news.
	Amount *KnownAmount
	// Expires, when set, is the moment the entry stops applying: the start of the
	// day after until, UTC.
	Expires time.Time
	Until   string // as written, for the report
}

// KnownAmount is the amount a known violation had when it was accepted.
type KnownAmount struct {
	Currency string
	Minor    int64
}

// knownFile is known.yaml as written.
type knownFile struct {
	Version int          `yaml:"version"`
	Known   []knownEntry `yaml:"known"`
}

type knownEntry struct {
	Check  string `yaml:"check"`
	ID     string `yaml:"id"`
	Reason string `yaml:"reason"`
	Amount string `yaml:"amount"`
	Until  string `yaml:"until"`
}

// readKnown reads known_file and indexes it by check and id. It runs after the
// defaults are applied, so it can check entries against the checks they name.
func (c *Config) readKnown(dir string) []error {
	if c.KnownFile == "" {
		return nil
	}
	path := filepath.Join(dir, c.KnownFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return []error{fmt.Errorf("known_file: %w", err)}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f knownFile
	if err := dec.Decode(&f); err != nil {
		return []error{fmt.Errorf("known_file %s: %w", c.KnownFile, err)}
	}
	checks := map[string]Check{}
	for _, ch := range c.Checks {
		checks[ch.Name] = ch
	}
	var errs []error
	if f.Version != 1 {
		errs = append(errs, fmt.Errorf("known_file %s: version: want 1, got %d", c.KnownFile, f.Version))
	}
	c.Known = map[string]map[string]Known{}
	for i, e := range f.Known {
		where := fmt.Sprintf("known_file %s: known[%d]", c.KnownFile, i)
		k, entryErrs := c.knownEntry(e, checks)
		for _, err := range entryErrs {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		}
		if len(entryErrs) > 0 {
			continue
		}
		if c.Known[k.Check] == nil {
			c.Known[k.Check] = map[string]Known{}
		}
		if _, dup := c.Known[k.Check][k.ID]; dup {
			errs = append(errs, fmt.Errorf("%s: check %s, id %s is listed twice", where, k.Check, k.ID))
			continue
		}
		c.Known[k.Check][k.ID] = k
	}
	return errs
}

func (c *Config) knownEntry(e knownEntry, checks map[string]Check) (Known, []error) {
	var errs []error
	k := Known{Check: e.Check, ID: e.ID, Reason: strings.TrimSpace(e.Reason), Until: e.Until}
	ch, ok := checks[e.Check]
	switch {
	case e.Check == "":
		errs = append(errs, errors.New("check: required"))
	case !ok:
		errs = append(errs, fmt.Errorf("check: no check is named %q", e.Check))
	}
	if e.ID == "" {
		errs = append(errs, errors.New("id: required, as the report shows it"))
	}
	if k.Reason == "" {
		errs = append(errs, errors.New("reason: required; say why this violation is accepted, so the next person does not have to guess"))
	}
	if e.Amount != "" {
		a, err := c.knownAmount(e.Amount)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("amount: %w", err))
		case ok && ch.Amount == nil:
			errs = append(errs, fmt.Errorf("amount: check %s has no amount", e.Check))
		default:
			k.Amount = &a
		}
	}
	if e.Until != "" {
		d, err := time.Parse(time.DateOnly, e.Until)
		if err != nil {
			errs = append(errs, fmt.Errorf("until: want a date like 2026-12-31, got %q", e.Until))
		}
		k.Expires = d.AddDate(0, 0, 1)
	}
	return k, errs
}

// knownAmount reads "26.99 USD".
func (c *Config) knownAmount(s string) (KnownAmount, error) {
	num, code, ok := strings.Cut(strings.TrimSpace(s), " ")
	code = strings.ToUpper(strings.TrimSpace(code))
	if !ok || code == "" {
		return KnownAmount{}, fmt.Errorf("want an amount and a currency like 26.99 USD, got %q", s)
	}
	exp, ok := c.Money.Exponent(code)
	if !ok {
		return KnownAmount{}, fmt.Errorf("unknown currency %q", code)
	}
	minor, err := money.Parse(num, exp, money.Format{})
	if err != nil {
		return KnownAmount{}, err
	}
	return KnownAmount{Currency: code, Minor: minor}, nil
}
