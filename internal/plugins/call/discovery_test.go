package call_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/call"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
)

func TestDiscoveryCacheSharesVerificationUntilExpiry(t *testing.T) {
	t.Setenv("QUIVR_PLUGIN_DISCOVERY_TTL", "")
	synctest.Test(t, func(t *testing.T) {
		pin := policyPin()
		pin.Endpoint = freshEndpoint()
		document := discoveryDocument(t, pin)
		release := make(chan struct{})
		var gets atomic.Int32
		defer devhost.SetTransport(nil)
		devhost.SetTransport(roundTrip(func(r *http.Request) (*http.Response, error) {
			gets.Add(1)
			<-release
			return wireResponse(http.StatusOK, document), nil
		}))
		const callers = 16
		results := make(chan error, callers)
		for range callers {
			go func() {
				_, err := call.Discover(t.Context(), pin)
				results <- err
			}()
		}
		synctest.Wait()
		requestsDuringFlight := gets.Load()
		close(release)
		for range callers {
			if err := <-results; err != nil {
				t.Error(err)
			}
		}
		if requestsDuringFlight != 1 {
			t.Fatalf("%d concurrent callers sent %d discovery requests, want one", callers, requestsDuringFlight)
		}
		for _, step := range []struct {
			advance time.Duration
			want    int32
		}{{29 * time.Second, 1}, {time.Second, 2}} {
			<-time.After(step.advance)
			served, err := call.Discover(t.Context(), pin)
			if err != nil || served != pin.PluginAPI() || gets.Load() != step.want {
				t.Fatalf("discovery API=%q, error=%v, requests=%d, want API %q and %d requests", served, err, gets.Load(), pin.PluginAPI(), step.want)
			}
		}
	})
}

func TestDiscoveryCacheWaiterKeepsItsOwnDeadline(t *testing.T) {
	t.Setenv("QUIVR_PLUGIN_DISCOVERY_TTL", "")
	synctest.Test(t, func(t *testing.T) {
		pin := policyPin()
		pin.Endpoint = freshEndpoint()
		document := discoveryDocument(t, pin)
		started := make(chan struct{})
		var gets atomic.Int32
		defer devhost.SetTransport(nil)
		devhost.SetTransport(roundTrip(func(r *http.Request) (*http.Response, error) {
			if gets.Add(1) == 1 {
				close(started)
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			return wireResponse(http.StatusOK, document), nil
		}))
		short, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		leader := make(chan error, 1)
		go func() {
			_, err := call.Discover(short, pin)
			leader <- err
		}()
		<-started
		waiter := make(chan error, 1)
		go func() {
			_, err := call.Discover(t.Context(), pin)
			waiter <- err
		}()
		synctest.Wait()
		requestsDuringFlight := gets.Load()
		if err := <-leader; !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("leader error = %v, want deadline exceeded", err)
		}
		if err := <-waiter; err != nil || requestsDuringFlight != 1 || gets.Load() != 2 {
			t.Fatalf("waiter error=%v, requests during flight=%d, total=%d; want successful takeover after one shared request", err, requestsDuringFlight, gets.Load())
		}
	})
}

func TestDiscoveryCacheTTLConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, ttl string
		want      int
	}{
		{name: "disabled", ttl: "0", want: 3},
		{name: "custom", ttl: "1s", want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("QUIVR_PLUGIN_DISCOVERY_TTL", tc.ttl)
			synctest.Test(t, func(t *testing.T) {
				pin := policyPin()
				pin.Endpoint = freshEndpoint()
				document := discoveryDocument(t, pin)
				gets := 0
				defer devhost.SetTransport(nil)
				devhost.SetTransport(roundTrip(func(r *http.Request) (*http.Response, error) {
					gets++
					return wireResponse(http.StatusOK, document), nil
				}))
				for i := range 3 {
					if i == 2 {
						<-time.After(time.Second)
					}
					if _, err := call.Discover(t.Context(), pin); err != nil {
						t.Fatal(err)
					}
				}
				if gets != tc.want {
					t.Fatalf("TTL %q sent %d discovery requests, want %d", tc.ttl, gets, tc.want)
				}
			})
		})
	}
}

func TestDiscoveryTransportFailureIsNotCached(t *testing.T) {
	pin := policyPin()
	pin.Endpoint = freshEndpoint()
	document := discoveryDocument(t, pin)
	gets := 0
	t.Cleanup(func() { devhost.SetTransport(nil) })
	devhost.SetTransport(roundTrip(func(r *http.Request) (*http.Response, error) {
		gets++
		if gets == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return wireResponse(http.StatusOK, document), nil
	}))
	if _, err := call.Discover(t.Context(), pin); !errors.Is(err, plugins.ErrUnavailable) {
		t.Fatalf("transport failure = %v, want plugin unavailable", err)
	}
	if served, err := call.Discover(t.Context(), pin); err != nil || served != pin.PluginAPI() || gets != 2 {
		t.Fatalf("retry API=%q, error=%v, requests=%d, want successful fresh discovery", served, err, gets)
	}
}

func TestContributionResponseInvalidatesDiscovery(t *testing.T) {
	t.Setenv("QUIVR_PLUGIN_DISCOVERY_TTL", "")
	for _, tc := range []struct {
		name       string
		status     int
		digest     []string
		fail       bool
		failRead   bool
		invalidate bool
		refuse     bool
	}{
		{name: "transport", fail: true, invalidate: true, refuse: true},
		{name: "read", status: 200, failRead: true, invalidate: true, refuse: true},
		{name: "unauthorized", status: 401, invalidate: true},
		{name: "redirect", status: 302, invalidate: true},
		{name: "forbidden", status: 403, invalidate: false},
		{name: "not-found", status: 404, invalidate: true},
		{name: "method-not-allowed", status: 405, invalidate: true},
		{name: "server-error", status: 503, invalidate: true},
		{name: "wrong-digest", status: 200, digest: []string{"sha256:" + strings.Repeat("b", 64)}, invalidate: true, refuse: true},
		{name: "empty-digest", status: 200, digest: []string{""}, invalidate: true, refuse: true},
		{name: "matching-digest", status: 200, digest: []string{policyPin().ManifestDigest}},
		{name: "absent-digest", status: 200},
		{name: "client-error", status: 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pin := policyPin()
			pin.Endpoint = freshEndpoint()
			document := discoveryDocument(t, pin)
			gets, posts := 0, 0
			t.Cleanup(func() { devhost.SetTransport(nil) })
			devhost.SetTransport(roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/v0/discovery" {
					gets++
					return wireResponse(http.StatusOK, document), nil
				}
				posts++
				if posts != 2 {
					return wireResponse(http.StatusOK, "{}"), nil
				}
				if tc.fail {
					return nil, io.ErrUnexpectedEOF
				}
				resp := wireResponse(tc.status, `{"code":"busy","message":"try again","retryable":true}`)
				if tc.failRead {
					resp.Body = io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF))
				}
				for _, digest := range tc.digest {
					resp.Header.Add("X-Quivr-Manifest-Digest", digest)
				}
				return resp, nil
			}))
			invoke := func(check call.Check) (*devhost.Result, error) {
				return call.Invoke(t.Context(), pin, call.EmbedQuery, call.Bytes([]byte("{}")), check, nil)
			}
			if _, err := invoke(nil); err != nil {
				t.Fatal(err)
			}
			checked := false
			result, err := invoke(func(context.Context, []byte) []plugins.Issue {
				checked = true
				return nil
			})
			if tc.refuse && (result != nil || !errors.Is(err, plugins.ErrUnavailable) || checked) {
				t.Fatalf("unverified response result=%v, error=%v, validated=%t", result, err, checked)
			}
			if !tc.refuse && tc.status == http.StatusOK && (err != nil || !checked) {
				t.Fatalf("valid response error=%v, validated=%t; want success", err, checked)
			}
			if gets != 1 {
				t.Fatalf("warm invocation sent discovery request %d, want one total", gets)
			}
			wantGets := 1
			if tc.invalidate {
				wantGets++
			}
			if _, err := invoke(nil); err != nil || gets != wantGets {
				t.Fatalf("next invocation error=%v, discovery requests=%d, want %d", err, gets, wantGets)
			}
		})
	}
}

func TestContributionFailureInvalidatesAnInFlightDiscoveryRefresh(t *testing.T) {
	t.Setenv("QUIVR_PLUGIN_DISCOVERY_TTL", "1s")
	synctest.Test(t, func(t *testing.T) {
		pin := policyPin()
		pin.Endpoint = freshEndpoint()
		document := discoveryDocument(t, pin)
		postStarted, failPost := make(chan struct{}), make(chan struct{})
		refreshStarted, finishRefresh := make(chan struct{}), make(chan struct{})
		var gets atomic.Int32
		defer devhost.SetTransport(nil)
		devhost.SetTransport(roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPost {
				close(postStarted)
				select {
				case <-failPost:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				return wireResponse(http.StatusServiceUnavailable, `{"code":"offline","message":"try again","retryable":true}`), nil
			}
			if gets.Add(1) == 2 {
				close(refreshStarted)
				select {
				case <-finishRefresh:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			return wireResponse(http.StatusOK, document), nil
		}))
		if _, err := call.Discover(t.Context(), pin); err != nil {
			t.Fatal(err)
		}
		post := make(chan error, 1)
		go func() {
			_, err := call.Invoke(t.Context(), pin, call.EmbedQuery, call.Bytes([]byte("{}")), nil, nil)
			post <- err
		}()
		synctest.Wait()
		select {
		case <-postStarted:
		default:
			t.Fatal("warm invocation did not reach POST")
		}
		<-time.After(time.Second)
		refresh := make(chan error, 1)
		go func() {
			_, err := call.Discover(t.Context(), pin)
			refresh <- err
		}()
		synctest.Wait()
		select {
		case <-refreshStarted:
		default:
			t.Fatal("expired discovery did not reach the peer")
		}
		close(failPost)
		var declared *plugins.PluginError
		if err := <-post; !errors.As(err, &declared) || declared.Status != http.StatusServiceUnavailable {
			t.Errorf("POST error=%v, want HTTP 503", err)
		}
		close(finishRefresh)
		if err := <-refresh; !errors.Is(err, plugins.ErrUnavailable) {
			t.Errorf("invalidated refresh error=%v, want unavailable", err)
		}
		if _, err := call.Discover(t.Context(), pin); err != nil || gets.Load() != 3 {
			t.Fatalf("next discovery error=%v, requests=%d; want a third GET", err, gets.Load())
		}
	})
}

func discoveryDocument(t *testing.T, pin *plugins.Pin) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"plugin_api":      pin.PluginAPI(),
		"plugin":          map[string]string{"id": pin.Manifest.ID, "version": pin.Manifest.Version},
		"manifest_digest": pin.ManifestDigest,
		"contributions":   pin.Manifest.Contributions.Names(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func wireResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

var testEndpointSequence atomic.Uint64

func freshEndpoint() string {
	return fmt.Sprintf("http://call-%d.test", testEndpointSequence.Add(1))
}
