package source

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/topology"
)

// Unreachable says whether err from Query means that the database could not be
// reached, rather than that the check went wrong: a refused or reset connection,
// a network error, no MongoDB server to select. A query that ran out of its
// timeout is the check's problem, a slow query, and is not unreachable. An error
// it does not recognise is not either, so the check fails on its own as before.
// See docs/adr/0007-source-down-one-message.md.
func Unreachable(err error) bool {
	if err == nil {
		return false
	}
	// MongoDB found no server to send the query to, also when it gave up
	// because the timeout ran out.
	var sel topology.ServerSelectionError
	if errors.As(err, &sel) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) {
		return false
	}
	var op *net.OpError
	var conn *pgconn.ConnectError
	return mongo.IsNetworkError(err) || errors.As(err, &op) || errors.As(err, &conn) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}
