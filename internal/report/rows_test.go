package report

import (
	"bytes"
	"testing"

	"github.com/ianfoxdev/money-checks/internal/check"
)

func TestCSV(t *testing.T) {
	var b bytes.Buffer
	c := NewCSV(&b, []string{"email", "note"}, true)
	usd := &check.Money{Currency: "USD", Minor: -3000, Exponent: 2}
	for _, s := range []check.Sample{
		{ID: "p1", Amount: usd, Fields: []check.Field{{Name: "email", Value: "a***@example.com"}, {Name: "note", Value: "a, \"b\"\nc"}}},
		{ID: "=HYPERLINK(\"x\")", Fields: []check.Field{{Name: "email", Value: "-"}, {Name: "note", Value: "+1 555"}}, Reason: "refunded, FIN-12"},
		{ID: "p3", Amount: &check.Money{Currency: "JPY", Minor: 1200}, Note: "known at 1000 JPY, the amount changed"},
	} {
		if err := c.Write(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "id,amount,currency,email,note,known,known_note\n" +
		"p1,-30.00,USD,a***@example.com,\"a, \"\"b\"\"\nc\",,\n" +
		"\"'=HYPERLINK(\"\"x\"\")\",,,-,'+1 555,\"refunded, FIN-12\",\n" +
		"p3,1200,JPY,,,,\"known at 1000 JPY, the amount changed\"\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}

func TestJSONLines(t *testing.T) {
	var b bytes.Buffer
	j := NewJSONLines(&b)
	for _, s := range []check.Sample{
		{ID: "p1", Amount: &check.Money{Currency: "USD", Minor: 1099, Exponent: 2}, Fields: []check.Field{{Name: "n", Value: "2"}}},
		{ID: "p2", Reason: "test account"},
	} {
		if err := j.Write(s); err != nil {
			t.Fatal(err)
		}
	}
	want := `{"id":"p1","amount":{"currency":"USD","minor":1099,"amount":"10.99"},"fields":{"n":"2"}}` + "\n" +
		`{"id":"p2","reason":"test account"}` + "\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}
