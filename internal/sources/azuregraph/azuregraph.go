// Package azuregraph turns Azure Resource Graph queries into subjects. Each
// query names the kind of subject its rows are, the column that keys a row,
// the column that titles it, and the fact the row is written as. One query
// can read resource settings or Defender's recommendations, so a pack can
// judge any of them. It signs in as the pod's workload identity, which
// needs Reader on what it queries. Azure Policy's own verdicts come through
// the azure_policy source, never a query here.
package azuregraph

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/azure"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/obs"
	"github.com/orchard-suite/trellis/internal/sources"
)

// Query is one Resource Graph query and how its rows become subjects.
type Query struct {
	Kind  string `yaml:"kind"`
	Query string `yaml:"query"`
	// Key and Title name the columns that key and title a row; id and
	// name unless set. The key is unique per row: the rows are read in its
	// order, and a read in which one repeats fails.
	Key   string `yaml:"key"`
	Title string `yaml:"title"`
	// Fact names the fact a row is written as; azure unless set.
	Fact string `yaml:"fact"`
}

// Source runs the queries.
type Source struct {
	api     *azure.Management
	scope   azure.Scope
	queries []Query
	log     *slog.Logger
}

// New makes the source. api reads the management API; scope narrows the
// queries to subscriptions or management groups, every one the identity
// may read when it names neither.
func New(api *azure.Management, scope azure.Scope, queries []Query, log *slog.Logger) (*Source, error) {
	if err := scope.Check(); err != nil {
		return nil, fmt.Errorf("azure_graph: %w", err)
	}
	for i, q := range queries {
		if q.Kind == "" || q.Query == "" {
			return nil, fmt.Errorf("azure_graph query %d needs a kind and a query", i)
		}
		if q.Key == "" {
			queries[i].Key = "id"
		}
		if q.Title == "" {
			queries[i].Title = "name"
		}
		if q.Fact == "" {
			queries[i].Fact = "azure"
		}
		if !azure.IsColumn(queries[i].Key) {
			return nil, fmt.Errorf("azure_graph query %d: key %q must be the bare name of a column the query projects", i, queries[i].Key)
		}
	}
	return &Source{api: api, scope: scope, queries: queries, log: log}, nil
}

func init() {
	sources.Register("azure_graph", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			Endpoint    string `yaml:"endpoint"`
			azure.Scope `yaml:",inline"`
			Queries     []Query `yaml:"queries"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		id, err := azure.IdentityFromEnv()
		if err != nil {
			return nil, err
		}
		api := azure.NewManagement(c.Endpoint, func(ctx context.Context) (string, error) { return azure.SignIn(ctx, id, azure.ManagementScope) }, obs.Client(60*time.Second))
		return New(api, c.Scope, c.Queries, env.Log)
	})
}

// Facts are the facts its queries write.
func (s *Source) Facts() []string {
	out := make([]string, 0, len(s.queries))
	for _, q := range s.queries {
		out = append(out, q.Fact)
	}
	return out
}

// Collect runs every query, page by page, and writes each row.
func (s *Source) Collect(ctx context.Context, e *estate.Writer) error {
	for _, q := range s.queries {
		rows, err := s.api.Query(ctx, s.scope, q.Query, q.Key)
		if err != nil {
			return fmt.Errorf("query for %s: %w", q.Kind, err)
		}
		n := Shape(e, q, rows)
		s.log.InfoContext(ctx, "azure graph read", "kind", q.Kind, "rows", n)
	}
	return nil
}

// Shape writes rows as subjects of the query's kind. A row without its key
// column is skipped.
func Shape(e *estate.Writer, q Query, rows []map[string]any) int {
	n := 0
	for _, row := range rows {
		key, _ := row[q.Key].(string)
		if key == "" {
			continue
		}
		title, _ := row[q.Title].(string)
		_ = e.Subject(q.Kind, strings.ToLower(key), title).Set(q.Fact, row)
		n++
	}
	return n
}
