// Package kubernetes reads the cluster itself: how each workload's pods
// run, whether their namespaces carry a NetworkPolicy, and what the policy
// engine's PolicyReports say about them. It works without a policy engine
// and says so.
package kubernetes

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources"
)

// Provider is the kubernetes source.
type Provider struct {
	client  *Client
	cluster string
	log     *slog.Logger
}

// New makes the source. cluster is the name the catalog uses for this
// cluster, so workloads merge with the ones the catalog adapter read.
func New(client *Client, cluster string, log *slog.Logger) *Provider {
	return &Provider{client: client, cluster: cluster, log: log}
}

func init() {
	sources.Register("kubernetes", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			URL     string `yaml:"url"`
			Token   string `yaml:"token"`
			CAFile  string `yaml:"ca_file"`
			Cluster string `yaml:"cluster"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		client, err := NewClient(Settings{URL: c.URL, Token: c.Token, CAFile: c.CAFile})
		if err != nil {
			return nil, err
		}
		if c.Cluster == "" {
			c.Cluster = "cluster"
		}
		return New(client, c.Cluster, env.Log), nil
	})
}

// Facts are what the source writes.
func (p *Provider) Facts() []string { return []string{"cluster", "workload", "pods"} }

// Snapshot is what one read of the API returned.
type Snapshot struct {
	Pods            []Pod
	ReplicaSets     []Owned
	Jobs            []Owned
	Namespaces      []Namespace
	NetworkPolicies []NetworkPolicy
	// PolicyReports is nil when no engine serves them.
	PolicyReports []PolicyReport
	PolicyEngine  bool
}

// Collect reads the API and shapes the snapshot into the estate.
func (p *Provider) Collect(ctx context.Context, e *estate.Writer) error {
	var s Snapshot
	reads := []struct {
		path string
		out  any
	}{
		{"/api/v1/pods", &s.Pods},
		{"/apis/apps/v1/replicasets", &s.ReplicaSets},
		{"/apis/batch/v1/jobs", &s.Jobs},
		{"/api/v1/namespaces", &s.Namespaces},
		{"/apis/networking.k8s.io/v1/networkpolicies", &s.NetworkPolicies},
	}
	for _, r := range reads {
		if err := p.client.List(ctx, r.path, r.out); err != nil {
			return err
		}
	}
	var reports, clusterReports []PolicyReport
	err := p.client.List(ctx, "/apis/wgpolicyk8s.io/v1alpha2/policyreports", &reports)
	switch {
	case errors.Is(err, ErrNotServed):
		p.log.InfoContext(ctx, "no policy engine: the PolicyReport API is not served")
	case err != nil:
		return err
	default:
		s.PolicyEngine = true
		if err := p.client.List(ctx, "/apis/wgpolicyk8s.io/v1alpha2/clusterpolicyreports", &clusterReports); err != nil && !errors.Is(err, ErrNotServed) {
			return err
		}
		s.PolicyReports = append(reports, clusterReports...)
	}
	counts, err := Shape(e, p.cluster, s)
	if err != nil {
		return err
	}
	p.log.InfoContext(ctx, "kubernetes read", "pods", len(s.Pods), "workloads", counts.Workloads, "namespaces", len(s.Namespaces), "network_policies", len(s.NetworkPolicies), "policy_engine", s.PolicyEngine, "policy_failures", counts.PolicyFailures)
	return nil
}

// Counts is what Shape added, for the log.
type Counts struct {
	Workloads, PolicyFailures int
}

// Pods is what a workload's pod specs say about how it runs, as the pods
// fact carries it. A workload is only as safe as its least safe pod.
type Pods struct {
	RunAsNonRoot   bool     `json:"run_as_non_root"`
	Privileged     bool     `json:"privileged"`
	HostNamespaces bool     `json:"host_namespaces"`
	NoLimits       []string `json:"no_limits"`
	NetworkPolicy  bool     `json:"network_policy"`
	// PolicyFailures is nil when no policy engine runs, so the rule that
	// reads it says nothing.
	PolicyFailures []PolicyFailure `json:"policy_failures"`
}

// PolicyFailure is one failed policy rule from a PolicyReport.
type PolicyFailure struct {
	Policy  string `json:"policy"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// Shape is a pure function from the API's objects to the estate: pods roll
// up to the workload that owns them, a namespace's policies mark every
// workload in it, and policy report failures land on the workload the
// failing resource belongs to. The cluster is a subject of its own.
func Shape(e *estate.Writer, cluster string, s Snapshot) (Counts, error) {
	var c Counts
	if err := e.Subject("cluster", cluster, cluster).Set("cluster", map[string]any{"name": cluster, "policy_engine": s.PolicyEngine}); err != nil {
		return c, err
	}

	rsOwner := map[string]OwnerReference{}
	for _, rs := range s.ReplicaSets {
		if o, ok := controller(rs.Metadata); ok {
			rsOwner[rs.Metadata.Namespace+"/"+rs.Metadata.Name] = o
		}
	}
	jobOwner := map[string]OwnerReference{}
	for _, j := range s.Jobs {
		if o, ok := controller(j.Metadata); ok {
			jobOwner[j.Metadata.Namespace+"/"+j.Metadata.Name] = o
		}
	}
	resolve := func(ns, kind, name string) (string, string) {
		switch kind {
		case "ReplicaSet":
			if o, ok := rsOwner[ns+"/"+name]; ok {
				return o.Kind, o.Name
			}
		case "Job":
			if o, ok := jobOwner[ns+"/"+name]; ok {
				return o.Kind, o.Name
			}
		}
		return kind, name
	}

	policed := map[string]bool{}
	for _, np := range s.NetworkPolicies {
		policed[np.Metadata.Namespace] = true
	}

	type acc struct {
		ns, name, kind string
		images         []string
		pods           *Pods
	}
	byKey := map[string]*acc{}
	var order []string
	podOwner := map[string]string{}
	for _, pod := range s.Pods {
		kind, name := "Pod", pod.Metadata.Name
		if o, ok := controller(pod.Metadata); ok {
			kind, name = resolve(pod.Metadata.Namespace, o.Kind, o.Name)
		}
		key := cluster + "/" + pod.Metadata.Namespace + "/" + name
		a, ok := byKey[key]
		if !ok {
			a = &acc{ns: pod.Metadata.Namespace, name: name, kind: kind, pods: &Pods{RunAsNonRoot: true, NoLimits: []string{}, NetworkPolicy: policed[pod.Metadata.Namespace]}}
			if s.PolicyEngine {
				a.pods.PolicyFailures = []PolicyFailure{}
			}
			byKey[key] = a
			order = append(order, key)
			c.Workloads++
		}
		podOwner[pod.Metadata.Namespace+"/"+pod.Metadata.Name] = key
		merge(a.pods, pod.Spec)
		for _, ctr := range append(append([]Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
			if ctr.Image != "" && !contains(a.images, ctr.Image) {
				a.images = append(a.images, ctr.Image)
			}
		}
	}

	if s.PolicyEngine {
		seen := map[string]bool{}
		for _, report := range s.PolicyReports {
			for _, r := range report.Results {
				if r.Result != "fail" {
					continue
				}
				refs := r.Resources
				if len(refs) == 0 && report.Scope != nil {
					refs = []ResourceRef{*report.Scope}
				}
				for _, ref := range refs {
					ns := ref.Namespace
					if ns == "" {
						ns = report.Metadata.Namespace
					}
					var key string
					if ref.Kind == "Pod" {
						key = podOwner[ns+"/"+ref.Name]
					} else {
						kind, name := resolve(ns, ref.Kind, ref.Name)
						if kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "CronJob" || kind == "Job" {
							key = cluster + "/" + ns + "/" + name
						}
					}
					a, ok := byKey[key]
					if !ok {
						continue
					}
					id := key + "|" + r.Policy + "|" + r.Rule
					if seen[id] {
						continue
					}
					seen[id] = true
					a.pods.PolicyFailures = append(a.pods.PolicyFailures, PolicyFailure{Policy: r.Policy, Rule: r.Rule, Message: strings.TrimSpace(r.Message)})
					c.PolicyFailures++
				}
			}
		}
	}

	for _, key := range order {
		a := byKey[key]
		sort.Strings(a.pods.NoLimits)
		w := e.Subject("workload", key, a.ns+"/"+a.name)
		images := make([]sources.Image, 0, len(a.images))
		for _, ref := range a.images {
			images = append(images, sources.ParseImage(ref))
			w.Link("image", ref)
		}
		if err := w.Set("workload", map[string]any{"cluster": cluster, "namespace": a.ns, "name": a.name, "kind": a.kind, "images": images}); err != nil {
			return c, err
		}
		if err := w.Set("pods", a.pods); err != nil {
			return c, err
		}
	}
	return c, nil
}

// merge folds one pod spec into its workload's facts: the workload is only
// as safe as its least safe pod.
func merge(f *Pods, spec PodSpec) {
	if spec.HostNetwork || spec.HostPID || spec.HostIPC {
		f.HostNamespaces = true
	}
	podNonRoot := spec.SecurityContext != nil && (boolOf(spec.SecurityContext.RunAsNonRoot) || (spec.SecurityContext.RunAsUser != nil && *spec.SecurityContext.RunAsUser > 0))
	for _, ctr := range append(append([]Container{}, spec.InitContainers...), spec.Containers...) {
		sc := ctr.SecurityContext
		if sc != nil && boolOf(sc.Privileged) {
			f.Privileged = true
		}
		ctrNonRoot := sc != nil && (boolOf(sc.RunAsNonRoot) || (sc.RunAsUser != nil && *sc.RunAsUser > 0))
		ctrRoot := sc != nil && sc.RunAsUser != nil && *sc.RunAsUser == 0
		if ctrRoot || (!podNonRoot && !ctrNonRoot) {
			f.RunAsNonRoot = false
		}
		if ctr.Resources.Limits["cpu"] == "" || ctr.Resources.Limits["memory"] == "" {
			if !contains(f.NoLimits, ctr.Name) {
				f.NoLimits = append(f.NoLimits, ctr.Name)
			}
		}
	}
}

func controller(m ObjectMeta) (OwnerReference, bool) {
	if len(m.OwnerReferences) == 0 {
		return OwnerReference{}, false
	}
	return m.OwnerReferences[0], true
}

func boolOf(b *bool) bool { return b != nil && *b }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
