package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// Test says which violations a check must find in a test database; see
// docs/adr/0005-expect-and-test.md.
type Test struct {
	Check      string
	Violations []TestViolation
}

// TestViolation is one row a check must report. Amount, when set, must match.
type TestViolation struct {
	ID     string
	Amount *KnownAmount
}

type testsFile struct {
	Version int         `yaml:"version"`
	Tests   []testEntry `yaml:"tests"`
}

type testEntry struct {
	Check string `yaml:"check"`
	// A pointer, so that a missing list is told from an empty one.
	Violations *[]testViolation `yaml:"violations"`
}

type testViolation struct {
	ID     string `yaml:"id"`
	Amount string `yaml:"amount"`
}

// LoadTests reads a tests file for this configuration. All problems are reported
// at once.
func (c Config) LoadTests(path string) ([]Test, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tests, err := c.ParseTests(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return tests, nil
}

// ParseTests decodes and checks a tests file against the checks of c.
func (c Config) ParseTests(data []byte) ([]Test, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f testsFile
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	checks := map[string]Check{}
	for _, ch := range c.Checks {
		checks[ch.Name] = ch
	}
	var errs []error
	if f.Version != 1 {
		errs = append(errs, fmt.Errorf("version: want 1, got %d", f.Version))
	}
	if len(f.Tests) == 0 {
		errs = append(errs, errors.New("tests: at least one is required"))
	}
	var tests []Test
	seen := map[string]bool{}
	for i, e := range f.Tests {
		where := fmt.Sprintf("tests[%d]", i)
		ch, ok := checks[e.Check]
		switch {
		case e.Check == "":
			errs = append(errs, fmt.Errorf("%s: check: required", where))
			continue
		case !ok:
			errs = append(errs, fmt.Errorf("%s: check: no check is named %q", where, e.Check))
			continue
		case seen[e.Check]:
			errs = append(errs, fmt.Errorf("%s: check %s is tested twice", where, e.Check))
			continue
		}
		seen[e.Check] = true
		where = "test " + e.Check
		if e.Violations == nil {
			errs = append(errs, fmt.Errorf("%s: violations: required; [] when the check must find nothing", where))
			continue
		}
		t := Test{Check: e.Check, Violations: []TestViolation{}}
		ids := map[string]bool{}
		for j, v := range *e.Violations {
			at := fmt.Sprintf("%s: violations[%d]", where, j)
			switch {
			case v.ID == "":
				errs = append(errs, fmt.Errorf("%s: id: required, as the report shows it", at))
				continue
			case ids[v.ID]:
				errs = append(errs, fmt.Errorf("%s: id %s is listed twice", at, v.ID))
				continue
			}
			ids[v.ID] = true
			tv := TestViolation{ID: v.ID}
			if v.Amount != "" {
				a, err := c.knownAmount(v.Amount)
				switch {
				case err != nil:
					errs = append(errs, fmt.Errorf("%s: amount: %w", at, err))
				case !ch.HasAmount():
					errs = append(errs, fmt.Errorf("%s: amount: check %s has no amount", at, e.Check))
				default:
					tv.Amount = &a
				}
			}
			t.Violations = append(t.Violations, tv)
		}
		tests = append(tests, t)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return tests, nil
}
