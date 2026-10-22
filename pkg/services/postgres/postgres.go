package postgres

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/bob"
)

// Port is the port PostgreSQL listens on, at the service's own loopback address.
const Port = 5432

// Options configures the PostgreSQL service.
type Options struct {
	// Version is the major version from nixpkgs ("17" → postgresql_17); empty = nixpkgs'
	// default postgresql. bob.lock pins it with the nixpkgs revision.
	Version string
	// Databases are created after the server started, before the test runs.
	Databases []string
}

var version = regexp.MustCompile(`^[0-9]{1,3}$`)

// Service is PostgreSQL as a loopback service of a test (R-FW-025). The test sees
// POSTGRES_ADDR, PGHOST, PGPORT, PGUSER (postgres, trust authentication) and DATABASE_URL
// (the first database of Options.Databases, else "postgres"). The data directory is fresh for
// every run and fsync is off.
//
//	golang.TestOpts{Services: []bob.Service{postgres.Service(p, postgres.Options{Version: "17", Databases: []string{"app"}})}}
func Service(p *bob.P, o Options) bob.Service {
	attr := "postgresql"
	if version.MatchString(o.Version) {
		attr += "_" + o.Version
	}
	return spec(o, tools(p, attr))
}

// spec builds the service from its tools (postgresql, bash, coreutils).
func spec(o Options, tools []bob.Tool) *bob.ServiceSpec {
	db := "postgres"
	if len(o.Databases) > 0 {
		db = o.Databases[0]
	}
	return &bob.ServiceSpec{
		Name:  "postgres",
		Start: []string{"bash", "-c", script(o.Databases)},
		Tools: tools,
		// The start script marks readiness after the databases exist.
		Ready: bob.CmdReady("bash", "-c", `[ -e "$HOME/ready" ]`),
		Env: map[string]string{
			"PGHOST": "$ADDR", "PGPORT": fmt.Sprint(Port), "PGUSER": "postgres",
			"DATABASE_URL": "postgres://postgres@$ADDR:" + fmt.Sprint(Port) + "/" + db + "?sslmode=disable",
		},
		Timeout: 30 * time.Second,
	}
}

// script is the start command: initdb, the server in the background on $ADDR, the databases,
// the ready mark, then wait; SIGTERM becomes a fast shutdown (SIGINT to the server).
func script(dbs []string) string {
	var b strings.Builder
	b.WriteString(`set -eu
data="$HOME/data"
initdb -D "$data" -U postgres --auth=trust --no-sync -E UTF8 --locale=C >"$HOME/initdb.log"
postgres -D "$data" -c listen_addresses="$ADDR" -c unix_socket_directories="$HOME" -p ` + fmt.Sprint(Port) + ` \
  -c fsync=off -c synchronous_commit=off -c full_page_writes=off &
pid=$!
trap 'kill -INT "$pid"; wait "$pid"; exit 0' TERM
until pg_isready -q -h "$ADDR" -p ` + fmt.Sprint(Port) + `; do sleep 0.02; done
`)
	for _, db := range dbs {
		b.WriteString(`createdb -h "$ADDR" -p ` + fmt.Sprint(Port) + ` -U postgres ` + quote(db) + "\n")
	}
	b.WriteString(`: >"$HOME/ready"
wait "$pid"
`)
	return b.String()
}

// quote quotes s for bash.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
