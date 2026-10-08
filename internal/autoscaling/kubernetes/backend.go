// Package kubernetes implements the autoscaler's replica backend through the
// scale subresource of one Deployment, authenticated as the pod's service
// account. Clusters with KEDA installed can scale on the same signal without it.
package kubernetes

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/autoscaling"
)

const serviceAccount = "/var/run/secrets/kubernetes.io/serviceaccount"

// name matches a Kubernetes DNS subdomain, which also keeps it a single path segment.
var name = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

type Backend struct {
	// URL is the Deployment's /scale subresource.
	URL string
	// TokenFile is read on every request because projected tokens rotate.
	TokenFile string
	Client    *http.Client
}

// InCluster targets a Deployment in the pod's own namespace with the API
// address, CA and token that Kubernetes provides to every pod.
func InCluster(deployment string, timeout time.Duration) (Backend, error) {
	if len(deployment) > 253 || !name.MatchString(deployment) {
		return Backend{}, errors.New("Kubernetes Deployment name is invalid")
	}
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return Backend{}, errors.New("Kubernetes backend must run in a pod: KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT are unset")
	}
	namespace, err := os.ReadFile(serviceAccount + "/namespace")
	if err != nil || !name.Match(bytes.TrimSpace(namespace)) {
		return Backend{}, errors.New("Kubernetes service account namespace unreadable or invalid")
	}
	ca, err := os.ReadFile(serviceAccount + "/ca.crt")
	pool := x509.NewCertPool()
	if err != nil || !pool.AppendCertsFromPEM(ca) {
		return Backend{}, errors.New("Kubernetes service account CA unreadable or invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client := autoscaling.NewHTTPClient(timeout)
	client.Transport = transport
	endpoint := url.URL{Scheme: "https", Host: net.JoinHostPort(host, port), Path: "/apis/apps/v1/namespaces/" + string(bytes.TrimSpace(namespace)) + "/deployments/" + deployment + "/scale"}
	return Backend{URL: endpoint.String(), TokenFile: serviceAccount + "/token", Client: client}, nil
}

// scale is autoscaling/v1 Scale. The API omits spec.replicas when it is zero.
type scale struct {
	Kind string `json:"kind"`
	Spec struct {
		Replicas int `json:"replicas"`
	} `json:"spec"`
}

func (b Backend) call(ctx context.Context, method string, body []byte) (int, error) {
	token, err := os.ReadFile(b.TokenFile)
	if err != nil || len(bytes.TrimSpace(token)) == 0 {
		return 0, errors.New("Kubernetes service account token unreadable")
	}
	req, err := http.NewRequestWithContext(ctx, method, b.URL, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("invalid Kubernetes scale URL")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "quivr-autoscaler")
	if body != nil {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
	var result scale
	if err = autoscaling.RequestJSON(b.Client, req, &result); err != nil {
		return 0, err
	}
	if result.Kind != "Scale" || result.Spec.Replicas < 0 || result.Spec.Replicas > 1<<31-1 {
		return 0, errors.New("Kubernetes scale response missing or invalid")
	}
	return result.Spec.Replicas, nil
}

// Replicas reads the desired count, which is what SetReplicas changes.
func (b Backend) Replicas(ctx context.Context) (int, error) {
	return b.call(ctx, http.MethodGet, nil)
}

func (b Backend) SetReplicas(ctx context.Context, count int) error {
	if count < 1 || count > 1<<31-1 {
		return errors.New("Kubernetes target replicas must be positive 32-bit integers")
	}
	applied, err := b.call(ctx, http.MethodPatch, fmt.Appendf(nil, `{"spec":{"replicas":%d}}`, count))
	if err != nil {
		return err
	}
	if applied != count {
		return errors.New("Kubernetes replica update not acknowledged")
	}
	return nil
}
