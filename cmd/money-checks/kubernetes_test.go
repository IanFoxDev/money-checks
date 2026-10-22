package main

import (
	"os"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// The configuration in the Kubernetes example parses, and puts the state file on
// the volume the Deployment mounts.
func TestKubernetesExample(t *testing.T) {
	b, err := os.ReadFile("../../examples/kubernetes/checks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Parse(b, "/etc/money-checks")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != 2 || c.Serve.StateFile != "/var/lib/money-checks/state.json" || c.Serve.Retry != time.Minute || c.Serve.Slack == nil {
		t.Errorf("checks %d, serve %+v", len(c.Checks), c.Serve)
	}
}
