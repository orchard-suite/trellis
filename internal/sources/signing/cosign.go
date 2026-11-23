package signing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sigstore/cosign/v3/pkg/cosign"
	"github.com/sigstore/cosign/v3/pkg/oci"
	ociremote "github.com/sigstore/cosign/v3/pkg/oci/remote"
	"github.com/sigstore/sigstore-go/pkg/root"
)

// Identity is who may sign: the OIDC issuer and a pattern over the
// certificate's subject, such as ^https://github.com/example/ for every
// GitHub Actions workflow in one organization.
type Identity struct {
	Issuer        string
	SubjectRegExp string
}

// Cosign verifies with the sigstore libraries against the public good
// instance's trusted root, fetched once through TUF.
type Cosign struct {
	trusted  root.TrustedMaterial
	identity Identity
	registry []ociremote.Option
}

// NewCosign fetches the trusted root. The registry is read with the
// process's Docker credentials (DOCKER_CONFIG), so private images work.
func NewCosign(identity Identity) (*Cosign, error) {
	trusted, err := cosign.TrustedRoot()
	if err != nil {
		return nil, fmt.Errorf("sigstore trusted root: %w", err)
	}
	return &Cosign{
		trusted:  trusted,
		identity: identity,
		registry: []ociremote.Option{ociremote.WithRemoteOptions(remote.WithAuthFromKeychain(authn.DefaultKeychain))},
	}, nil
}

// Verify answers for one image. A registry or verification error is
// reported, not guessed at. Newer cosign stores signatures and
// attestations as sigstore bundles beside the image; older ones as .sig
// and .att tags. Both are read, so an image signed either way counts.
func (c *Cosign) Verify(ctx context.Context, reference string) Signature {
	ref, err := name.ParseReference(reference)
	if err != nil {
		return Signature{Error: err.Error()}
	}
	registry := append(append([]ociremote.Option{}, c.registry...), ociremote.WithRemoteOptions(remote.WithAuthFromKeychain(authn.DefaultKeychain), remote.WithContext(ctx)))
	// A plain signature's payload names the digest itself; an attestation
	// is an in-toto statement whose subject names it. Each needs its own
	// claim verifier, or the match fails as "missing digest in claim".
	opts := func(bundles, attestations bool) *cosign.CheckOpts {
		co := &cosign.CheckOpts{
			TrustedMaterial:    c.trusted,
			Identities:         []cosign.Identity{{Issuer: c.identity.Issuer, SubjectRegExp: c.identity.SubjectRegExp}},
			RegistryClientOpts: registry,
			ClaimVerifier:      cosign.SimpleClaimVerifier,
			NewBundleFormat:    bundles,
		}
		if bundles || attestations {
			co.ClaimVerifier = cosign.IntotoSubjectClaimVerifier
		}
		return co
	}

	var out Signature
	bundles, _, err := cosign.GetBundles(ctx, ref, registry)
	if err != nil && !isUnsigned(err) {
		return Signature{Error: trim(err)}
	}
	if len(bundles) > 0 {
		// A bundle is always a DSSE statement, so one call verifies the
		// signature and every attestation.
		atts, _, err := cosign.VerifyImageAttestations(ctx, ref, opts(true, true))
		switch {
		case err == nil && len(atts) > 0:
			out.Signed = true
			out.Identity = identity(atts[0])
			out.SBOM = anySBOM(atts)
		case err != nil && !isUnsigned(err) && !isUnattested(err):
			return Signature{Error: trim(err)}
		}
	}

	if !out.Signed {
		sigs, _, err := cosign.VerifyImageSignatures(ctx, ref, opts(false, false))
		switch {
		case err == nil && len(sigs) > 0:
			out.Signed = true
			out.Identity = identity(sigs[0])
		case err == nil || isUnsigned(err):
			return out
		default:
			return Signature{Error: trim(err)}
		}
	}
	if !out.SBOM {
		atts, _, err := cosign.VerifyImageAttestations(ctx, ref, opts(false, true))
		switch {
		case err == nil:
			out.SBOM = anySBOM(atts)
		case isUnattested(err) || isUnsigned(err):
		default:
			out.Error = "attestations: " + trim(err)
		}
	}
	return out
}

func identity(sig oci.Signature) string {
	cert, err := sig.Cert()
	if err != nil || cert == nil {
		return ""
	}
	if len(cert.URIs) > 0 {
		return cert.URIs[0].String()
	}
	if len(cert.EmailAddresses) > 0 {
		return cert.EmailAddresses[0]
	}
	return ""
}

func anySBOM(atts []oci.Signature) bool {
	for _, att := range atts {
		if payload, err := att.Payload(); err == nil && isSBOM(predicateType(payload)) {
			return true
		}
	}
	return false
}

// isUnsigned is the family of errors that mean "nothing to verify" rather
// than "could not verify".
func isUnsigned(err error) bool {
	var noMatch *cosign.ErrNoMatchingSignatures
	var none *cosign.ErrNoSignaturesFound
	var noTag *cosign.ErrImageTagNotFound
	if errors.As(err, &noMatch) || errors.As(err, &none) || errors.As(err, &noTag) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no signatures found") || strings.Contains(msg, "no matching signatures") || strings.Contains(msg, "no valid bundles") || strings.Contains(msg, "MANIFEST_UNKNOWN")
}

func isUnattested(err error) bool {
	var noMatch *cosign.ErrNoMatchingAttestations
	if errors.As(err, &noMatch) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no attestations") || strings.Contains(msg, "no matching attestations") || strings.Contains(msg, "MANIFEST_UNKNOWN")
}

// predicateType reads the in-toto predicate type out of an attestation
// payload, which is a DSSE envelope around a statement, or the statement.
func predicateType(payload []byte) string {
	var env struct {
		PayloadType   string `json:"payloadType"`
		Payload       string `json:"payload"`
		PredicateType string `json:"predicateType"`
	}
	if json.Unmarshal(payload, &env) != nil {
		return ""
	}
	if env.PredicateType != "" {
		return env.PredicateType
	}
	raw, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return ""
	}
	var st struct {
		PredicateType string `json:"predicateType"`
	}
	_ = json.Unmarshal(raw, &st)
	return st.PredicateType
}

// isSBOM is whether a predicate type is a software bill of materials:
// SPDX, CycloneDX, or syft's own.
func isSBOM(predicate string) bool {
	p := strings.ToLower(predicate)
	return strings.Contains(p, "spdx") || strings.Contains(p, "cyclonedx") || strings.Contains(p, "syft")
}

func trim(err error) string {
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}
