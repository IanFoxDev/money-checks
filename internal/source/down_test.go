package source

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/topology"

	"github.com/ianfoxdev/money-checks/internal/config"
)

func TestUnreachable(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused", fmt.Errorf("clickhouse: %w", refused), true},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		{"server selection", topology.ServerSelectionError{Wrapped: context.DeadlineExceeded}, true},
		{"slow query", fmt.Errorf("query: %w", context.DeadlineExceeded), false},
		{"syntax", errors.New("Syntax error: failed at position 7"), false},
		{"nil", nil, false},
	} {
		if got := Unreachable(c.err); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// What each driver returns when nothing listens on the port is unreachable, so
// a database that is away is one line in Slack and not a failure per check.
func TestOpenUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	t.Setenv("MC_DOWN_MONGO", "mongodb://"+addr+"/?serverSelectionTimeoutMS=300&connectTimeoutMS=300")
	t.Setenv("MC_DOWN_PG", "postgres://u:p@"+addr+"/db?connect_timeout=1")
	t.Setenv("MC_DOWN_CH", "http://"+addr+"/")
	for name, s := range map[string]config.Source{
		"mongodb":    {MongoDB: &config.MongoDB{URIEnv: "MC_DOWN_MONGO", Database: "shop"}},
		"postgres":   {Postgres: &config.Postgres{DSNEnv: "MC_DOWN_PG"}},
		"clickhouse": {ClickHouse: &config.ClickHouse{URLEnv: "MC_DOWN_CH", Database: "stats"}},
	} {
		_, err := Open(context.Background(), name, s, func(string) {})
		if err == nil || !Unreachable(err) {
			t.Errorf("%s: %v is not unreachable", name, err)
		}
	}
}
