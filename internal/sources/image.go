package sources

import "strings"

// Image is a container image reference taken apart, as the image and
// workload facts carry it.
type Image struct {
	Ref      string `json:"ref"`
	Registry string `json:"registry"`
	Tag      string `json:"tag"`
	// Pinned is a reference by digest, which never moves.
	Pinned bool `json:"pinned"`
}

// ParseImage takes a reference apart. One without a registry host is
// Docker Hub; one without a tag has an empty tag.
func ParseImage(ref string) Image {
	img := Image{Ref: ref, Registry: "docker.io", Pinned: strings.Contains(ref, "@sha256:")}
	name := ref
	if at := strings.Index(name, "@"); at >= 0 {
		name = name[:at]
	}
	if slash := strings.Index(name, "/"); slash >= 0 {
		host := name[:slash]
		if strings.ContainsAny(host, ".:") || host == "localhost" {
			img.Registry = host
			name = name[slash+1:]
		}
	}
	if colon := strings.LastIndex(name, ":"); colon >= 0 {
		img.Tag = name[colon+1:]
	}
	return img
}
