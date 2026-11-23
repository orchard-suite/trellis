package kubernetes

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

const (
	podsJSON = `{"items":[
	 {"metadata":{"name":"web-7d9f-abc","namespace":"apps","ownerReferences":[{"kind":"ReplicaSet","name":"web-7d9f"}]},
	  "spec":{"securityContext":{"runAsNonRoot":true},"containers":[{"name":"web","image":"ghcr.io/example/web:1.2.0","resources":{"limits":{"cpu":"1","memory":"256Mi"}}}]},"status":{"phase":"Running"}},
	 {"metadata":{"name":"agent-x1","namespace":"system","ownerReferences":[{"kind":"DaemonSet","name":"agent"}]},
	  "spec":{"hostNetwork":true,"containers":[{"name":"agent","image":"quay.io/example/agent:latest","securityContext":{"privileged":true},"resources":{"limits":{"memory":"64Mi"}}}]},"status":{"phase":"Running"}},
	 {"metadata":{"name":"sweep-29-k2","namespace":"apps","ownerReferences":[{"kind":"Job","name":"sweep-29"}]},
	  "spec":{"containers":[{"name":"sweep","image":"ghcr.io/example/sweep:0.3","securityContext":{"runAsUser":1000},"resources":{"limits":{"cpu":"100m","memory":"64Mi"}}}]},"status":{"phase":"Succeeded"}},
	 {"metadata":{"name":"lonely","namespace":"apps"},
	  "spec":{"containers":[{"name":"c","image":"busybox","resources":{}}]},"status":{"phase":"Running"}}
	]}`
	replicaSetsJSON = `{"items":[{"metadata":{"name":"web-7d9f","namespace":"apps","ownerReferences":[{"kind":"Deployment","name":"web"}]}}]}`
	jobsJSON        = `{"items":[{"metadata":{"name":"sweep-29","namespace":"apps","ownerReferences":[{"kind":"CronJob","name":"sweep"}]}}]}`
	namespacesJSON  = `{"items":[{"metadata":{"name":"apps"}},{"metadata":{"name":"system"}}]}`
	netpolsJSON     = `{"items":[{"metadata":{"name":"default-deny","namespace":"apps"}}]}`
	reportsJSON     = `{"items":[
	 {"metadata":{"name":"r1","namespace":"apps"},"scope":{"kind":"Pod","name":"web-7d9f-abc","namespace":"apps"},
	  "results":[{"policy":"require-limits","rule":"limits","result":"pass"},{"policy":"disallow-latest","rule":"tag","result":"fail","message":"tag required"}]},
	 {"metadata":{"name":"r2","namespace":"apps"},"results":[{"policy":"disallow-latest","rule":"tag","result":"fail","resources":[{"kind":"Deployment","name":"web","namespace":"apps"}]}]},
	 {"metadata":{"name":"r3","namespace":"system"},"scope":{"kind":"Pod","name":"agent-x1","namespace":"system"},
	  "results":[{"policy":"disallow-privileged","rule":"priv","result":"fail","message":"privileged"}]}
	]}`
)

func api(t *testing.T, policyEngine bool) *Client {
	t.Helper()
	routes := map[string]string{
		"/api/v1/pods":                                       podsJSON,
		"/apis/apps/v1/replicasets":                          replicaSetsJSON,
		"/apis/batch/v1/jobs":                                jobsJSON,
		"/api/v1/namespaces":                                 namespacesJSON,
		"/apis/networking.k8s.io/v1/networkpolicies":         netpolsJSON,
		"/apis/wgpolicyk8s.io/v1alpha2/clusterpolicyreports": `{"items":[]}`,
	}
	if policyEngine {
		routes["/apis/wgpolicyk8s.io/v1alpha2/policyreports"] = reportsJSON
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sa" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			http.Error(w, `{"kind":"Status","code":404}`, http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(Settings{URL: srv.URL, Token: "sa"})
	if err != nil {
		t.Fatal(err)
	}
	c.http = srv.Client()
	return c
}

func collect(t *testing.T, policyEngine bool) *estate.Estate {
	t.Helper()
	e := estate.New(time.Now())
	p := New(api(t, policyEngine), "cluster-a", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := p.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	return e
}

// facts reads a workload's facts back into their shapes.
func facts(t *testing.T, e *estate.Estate, key string) (kind string, pods *Pods) {
	t.Helper()
	s, ok := e.Find("workload", key)
	if !ok {
		t.Fatalf("no workload %s", key)
	}
	w := s.Facts["workload"].Value.(map[string]any)
	raw, _ := json.Marshal(s.Facts["pods"].Value)
	if s.Facts["pods"].Value != nil {
		pods = &Pods{}
		if err := json.Unmarshal(raw, pods); err != nil {
			t.Fatal(err)
		}
	}
	return w["kind"].(string), pods
}

func TestShapeWorkloads(t *testing.T) {
	e := collect(t, true)
	c, ok := e.Find("cluster", "cluster-a")
	if !ok || c.Facts["cluster"].Value.(map[string]any)["policy_engine"] != true {
		t.Fatalf("cluster: %+v", c)
	}
	if n := len(e.Of("workload")); n != 4 {
		t.Fatalf("workloads: %d, want web, agent, sweep, lonely", n)
	}
	kind, web := facts(t, e, "cluster-a/apps/web")
	if kind != "Deployment" || !web.RunAsNonRoot || len(web.NoLimits) != 0 || !web.NetworkPolicy {
		t.Errorf("web: %s %+v", kind, web)
	}
	if n := len(web.PolicyFailures); n != 1 || web.PolicyFailures[0].Policy != "disallow-latest" {
		t.Errorf("web failures should be one, deduplicated across the pod and deployment reports: %+v", web.PolicyFailures)
	}
	kind, agent := facts(t, e, "cluster-a/system/agent")
	if kind != "DaemonSet" || !agent.Privileged || !agent.HostNamespaces || agent.RunAsNonRoot || len(agent.NoLimits) != 1 || agent.NoLimits[0] != "agent" || agent.NetworkPolicy {
		t.Errorf("agent: %s %+v", kind, agent)
	}
	if len(agent.PolicyFailures) != 1 || agent.PolicyFailures[0].Message != "privileged" {
		t.Errorf("agent failures: %+v", agent.PolicyFailures)
	}
	if kind, sweep := facts(t, e, "cluster-a/apps/sweep"); kind != "CronJob" || !sweep.RunAsNonRoot {
		t.Errorf("sweep: %s %+v", kind, sweep)
	}
	if kind, lonely := facts(t, e, "cluster-a/apps/lonely"); kind != "Pod" || lonely.RunAsNonRoot || len(lonely.NoLimits) != 1 || lonely.PolicyFailures == nil || len(lonely.PolicyFailures) != 0 {
		t.Errorf("lonely: %s %+v", kind, lonely)
	}
}

func TestWithoutPolicyEngine(t *testing.T) {
	e := collect(t, false)
	c, _ := e.Find("cluster", "cluster-a")
	if c.Facts["cluster"].Value.(map[string]any)["policy_engine"] != false {
		t.Fatal("no PolicyReport API means no engine")
	}
	for _, w := range e.Of("workload") {
		pods, ok := w.Facts["pods"].Value.(map[string]any)
		if !ok {
			t.Errorf("%s lost its pod facts", w.Key)
			continue
		}
		if pods["policy_failures"] != nil {
			t.Errorf("%s has policy data without an engine", w.Key)
		}
	}
}

func TestEnrichesTheCatalogsWorkload(t *testing.T) {
	e := estate.New(time.Now())
	prior := e.Writer("test").Subject("workload", "cluster-a/apps/web", "apps/web")
	prior.SetRef(estate.Ref{Blueprint: "k8s_workload", Key: "cluster-a/apps/web"})
	p := New(api(t, true), "cluster-a", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := p.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	if w, _ := e.Find("workload", "cluster-a/apps/web"); w != prior.Subject || w.Ref == nil || w.Facts["pods"].Value == nil || w.Facts["pods"].Source != "test" {
		t.Errorf("the cluster read must enrich the catalog's workload, not replace it: %+v", w)
	}
}
