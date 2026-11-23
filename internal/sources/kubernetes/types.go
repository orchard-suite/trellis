package kubernetes

// The few fields trellis reads from each resource.

type ObjectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	Labels          map[string]string `json:"labels"`
	OwnerReferences []OwnerReference  `json:"ownerReferences"`
}

type OwnerReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Pod struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
	Status   struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type PodSpec struct {
	HostNetwork     bool             `json:"hostNetwork"`
	HostPID         bool             `json:"hostPID"`
	HostIPC         bool             `json:"hostIPC"`
	SecurityContext *SecurityContext `json:"securityContext"`
	InitContainers  []Container      `json:"initContainers"`
	Containers      []Container      `json:"containers"`
}

type SecurityContext struct {
	RunAsNonRoot *bool  `json:"runAsNonRoot"`
	RunAsUser    *int64 `json:"runAsUser"`
	Privileged   *bool  `json:"privileged"`
}

type Container struct {
	Name            string           `json:"name"`
	Image           string           `json:"image"`
	SecurityContext *SecurityContext `json:"securityContext"`
	Resources       struct {
		Limits map[string]string `json:"limits"`
	} `json:"resources"`
}

// Owned is any resource whose owner chain trellis follows: a ReplicaSet
// or a Job.
type Owned struct {
	Metadata ObjectMeta `json:"metadata"`
}

type Namespace struct {
	Metadata ObjectMeta `json:"metadata"`
}

type NetworkPolicy struct {
	Metadata ObjectMeta `json:"metadata"`
}

// PolicyReport is wgpolicyk8s.io/v1alpha2 PolicyReport or ClusterPolicyReport.
type PolicyReport struct {
	Metadata ObjectMeta     `json:"metadata"`
	Scope    *ResourceRef   `json:"scope"`
	Results  []PolicyResult `json:"results"`
}

type PolicyResult struct {
	Policy    string        `json:"policy"`
	Rule      string        `json:"rule"`
	Result    string        `json:"result"`
	Message   string        `json:"message"`
	Resources []ResourceRef `json:"resources"`
}

type ResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}
