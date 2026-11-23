package alertrules

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

// store stands in for Prometheus: each selector's series count and the
// labels its series carry.
func store(known map[string]Selector) func(string) Selector {
	return func(sel string) Selector { return known[sel] }
}

func TestAnalyzeKeepsWhatTheQueryKeeps(t *testing.T) {
	look := store(map[string]Selector{
		`kube_statefulset_status_replicas_ready{namespace="ledger",statefulset="clickhouse"}`: {Series: 1, Labels: []string{"instance", "job", "namespace", "pod", "statefulset"}},
		`longhorn_volume_last_backup_at`: {Series: 20, Labels: []string{"instance", "node", "pod", "pvc", "pvc_namespace", "service", "volume"}},
		`kube_persistentvolumeclaim_labels{label_recurring_job_group_longhorn_io_no_backups="enabled"}`: {Series: 9, Labels: []string{"namespace", "persistentvolumeclaim"}},
		`kube_job_failed{condition="true"}`:    {Series: 3, Labels: []string{"condition", "instance", "job_name", "namespace"}},
		`kube_job_owner{owner_kind="CronJob"}`: {Series: 3, Labels: []string{"job_name", "namespace", "owner_kind", "owner_name"}},
	})
	cases := []struct {
		name, query    string
		keeps, mayKeep []string
	}{
		{"max drops every label", `max(kube_statefulset_status_replicas_ready{namespace="ledger", statefulset="clickhouse"}) < 1`, []string{}, []string{}},
		{"absent keeps its equality matchers", `absent(trellis_build_info{job="trellis"})`, []string{"job"}, []string{"job"}},
		{"a filter keeps the left side's labels", `longhorn_volume_last_backup_at == 0 unless on (pvc, pvc_namespace) label_replace(kube_persistentvolumeclaim_labels{label_recurring_job_group_longhorn_io_no_backups="enabled"}, "pvc", "$1", "persistentvolumeclaim", "(.+)")`,
			[]string{"instance", "node", "pod", "pvc", "pvc_namespace", "service", "volume"}, []string{"instance", "node", "pod", "pvc", "pvc_namespace", "service", "volume"}},
		{"by keeps what it names", `max by (volume, pvc, pvc_namespace, service) (longhorn_volume_last_backup_at) == 0`, []string{"pvc", "pvc_namespace", "service", "volume"}, []string{"pvc", "pvc_namespace", "service", "volume"}},
		{"or keeps on every alert only what both sides carry", `max by (namespace, job_name) (kube_job_failed{condition="true"}) or max by (namespace) (kube_job_owner{owner_kind="CronJob"})`, []string{"namespace"}, []string{"job_name", "namespace"}},
		{"vector carries nothing", `(max(kube_job_failed{condition="true"}) or vector(0)) < 1`, []string{}, []string{}},
		{"group_left adds the included labels", `kube_job_failed{condition="true"} * on (namespace, job_name) group_left (owner_name) kube_job_owner{owner_kind="CronJob"}`,
			[]string{"condition", "instance", "job_name", "namespace", "owner_name"}, []string{"condition", "instance", "job_name", "namespace", "owner_name"}},
		{"histogram_quantile drops le", `histogram_quantile(0.99, sum by (node, le) (rate(longhorn_volume_last_backup_at[5m]))) > 1`, []string{"node"}, []string{"node"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := Analyze(c.query, look)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a.Keeps, c.keeps) || !reflect.DeepEqual(a.MayKeep, c.mayKeep) {
				t.Errorf("keeps %v may keep %v, want %v and %v", a.Keeps, a.MayKeep, c.keeps, c.mayKeep)
			}
		})
	}
}

func TestShapeWritesOneSubjectPerRule(t *testing.T) {
	est := estate.New(time.Now())
	w := est.Writer("alertrules")
	look := store(map[string]Selector{`gha_max_runners{namespace="arc-runners"}`: {Series: 0}})
	Shape(w, []Rule{
		{Name: "ActionsRunnersSilent", Group: "actions-runners", File: "/etc/rules/actions.yaml", Query: `absent_over_time(gha_max_runners{namespace="arc-runners"}[15m])`, Duration: 900, Labels: map[string]string{"severity": "critical"}},
		{Name: "ActionsRunnersSilent", Group: "actions-runners", Query: `absent_over_time(gha_max_runners{namespace="arc-runners"}[15m])`},
		{Name: "Broken", Group: "g", Query: `sum(`},
	}, look)
	subs := est.Of(Kind)
	if len(subs) != 3 {
		t.Fatalf("subjects %d", len(subs))
	}
	first := subs[0].Facts["alert"].Value.(map[string]any)
	if subs[0].Key != "actions-runners/ActionsRunnersSilent" || fmt.Sprint(first["for_seconds"]) != "900" {
		t.Errorf("first: %s %+v", subs[0].Key, first)
	}
	sels := first["selectors"].([]any)
	if len(sels) != 1 || fmt.Sprint(sels[0].(map[string]any)["series"]) != "0" {
		t.Errorf("the empty selector is kept with its count: %+v", sels)
	}
	if subs[1].Key != "actions-runners/ActionsRunnersSilent#2" {
		t.Errorf("a second rule of the same name: %s", subs[1].Key)
	}
	if _, ok := subs[2].Facts["alert"].Value.(map[string]any)["parse_error"]; !ok {
		t.Errorf("a query that does not parse keeps its error: %+v", subs[2].Facts)
	}
}

func TestAnUnlessRightSideIsOptional(t *testing.T) {
	look := store(map[string]Selector{`kube_job_status_failed`: {Series: 3, Labels: []string{"job_name", "namespace"}}})
	a, err := Analyze(`kube_job_status_failed > 0 unless on (namespace, job_name) kube_job_labels{label_excluded_from_alerts="true"}`, look)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Selectors) != 2 || a.Selectors[0].Optional || !a.Selectors[1].Optional {
		t.Errorf("the exclusion is optional, the alert's own selector is not: %+v", a.Selectors)
	}
}
