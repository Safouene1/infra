package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/opt"
	"gotest.tools/v3/poll"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/infrahq/infra/internal"
	"github.com/infrahq/infra/internal/certs"
	"github.com/infrahq/infra/internal/cmd/types"
	"github.com/infrahq/infra/internal/connector"
	"github.com/infrahq/infra/internal/server"
	"github.com/infrahq/infra/internal/server/data"
	"github.com/infrahq/infra/internal/server/models"
)

func TestConnector_Run(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for short run")
	}

	dir := t.TempDir()
	serverOpts := defaultServerOptions(dir)
	setupServerOptions(t, &serverOpts)
	serverOpts.Config = server.Config{
		Users: []server.User{
			{Name: "admin@example.com", AccessKey: "0000000001.adminadminadminadmin1234"},
			{Name: "connector", AccessKey: "0000000002.connectorconnectorconnec"},
		},
		Grants: []server.Grant{
			{User: "user1@example.com", Resource: "testing.ns1", Role: "admin"},
			{User: "user2@example.com", Resource: "testing", Role: "view"},
			{Group: "group1@example.com", Resource: "testing.ns1", Role: "logs"},
		},
	}

	srv, err := server.New(serverOpts)
	assert.NilError(t, err)

	fakeKube := &fakeKubeAPI{t: t}
	kubeSrv := httptest.NewTLSServer(fakeKube)
	t.Cleanup(kubeSrv.Close)

	ctx := context.Background()
	runAndWait(ctx, t, srv.Run)

	opts := connector.Options{
		Server: connector.ServerOptions{
			URL:                srv.Addrs.HTTPS.String(),
			AccessKey:          "0000000002.connectorconnectorconnec",
			TrustedCertificate: serverOpts.TLS.Certificate,
		},
		Name:         "testing",
		CACert:       types.StringOrFile(readFile(t, "testdata/pki/connector.crt")),
		CAKey:        types.StringOrFile(readFile(t, "testdata/pki/connector.key")),
		EndpointAddr: types.HostPort{Host: "127.0.0.1", Port: 55555},
		Kubernetes: connector.KubernetesOptions{
			AuthToken: "auth-token",
			Addr:      kubeSrv.URL,
			CA:        types.StringOrFile(certs.PEMEncodeCertificate(kubeSrv.Certificate().Raw)),
		},
		Addr: connector.ListenerOptions{
			HTTPS:   "127.0.0.1:0",
			Metrics: "127.0.0.1:0",
		},
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	t.Cleanup(cancel)

	runAndWait(ctx, t, func(ctx context.Context) error {
		return connector.Run(ctx, opts)
	})

	// check destination has been registered
	var destination *models.Destination
	poll.WaitOn(t, func(t poll.LogT) poll.Result {
		destination, err = data.GetDestination(srv.DB(), data.ByName("testing"))
		switch {
		case errors.Is(err, internal.ErrNotFound):
			return poll.Continue("destination not registered")
		case err != nil:
			return poll.Error(err)
		}
		return poll.Success()
	})

	// check the destination was updated
	expected := &models.Destination{
		Model: models.Model{
			ID:        anyUID,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		OrganizationMember: models.OrganizationMember{OrganizationID: srv.DB().OrganizationID()},
		Name:               "testing",
		UniqueID:           "4ebfd7dabeec5b37eafd20e3775f70ab86c7422036367d77d9bebfa03864e08b",
		ConnectionURL:      "127.0.0.1:55555",
		ConnectionCA:       opts.CACert.String(),
		LastSeenAt:         time.Now(),
		Version:            "99.99.99999",
		Resources:          models.CommaSeparatedStrings{"default", "ns1", "ns2"},
		Roles:              models.CommaSeparatedStrings{"admin", "view", "edit", "custom", "logs"},
	}
	assert.DeepEqual(t, destination, expected, cmpDestinationModel)

	poll.WaitOn(t, func(t poll.LogT) poll.Result {
		fakeKube.writesLock.Lock()
		defer fakeKube.writesLock.Unlock()
		if len(fakeKube.writes) >= 3 {
			return poll.Success()
		}
		return poll.Continue("request count %d waiting for 3", len(fakeKube.writes))
	})

	// check kube bindings were updated
	expectedWrites := []*http.Request{
		{
			Method: "PUT",
			URL:    &url.URL{Path: "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/infra:view"},
		},
		{
			Method: "PUT",
			URL:    &url.URL{Path: "/apis/rbac.authorization.k8s.io/v1/namespaces/ns1/rolebindings/infra:admin"},
		},
		{
			Method: "PUT",
			URL:    &url.URL{Path: "/apis/rbac.authorization.k8s.io/v1/namespaces/ns1/rolebindings/infra:logs"},
		},
	}
	sort.Slice(fakeKube.writes, func(i, j int) bool {
		return fakeKube.writes[i].URL.Path < fakeKube.writes[j].URL.Path
	})
	assert.DeepEqual(t, fakeKube.writes, expectedWrites, cmpHTTPRequestShallow)

	// TODO: check proxy is listening
}

var cmpDestinationModel = cmp.Options{
	cmp.FilterPath(opt.PathField(models.Model{}, "ID"), cmpIDNotZero),
	cmp.FilterPath(opt.PathField(models.Model{}, "CreatedAt"),
		opt.TimeWithThreshold(5*time.Second)),
	cmp.FilterPath(opt.PathField(models.Model{}, "UpdatedAt"),
		opt.TimeWithThreshold(5*time.Second)),
	cmp.FilterPath(opt.PathField(models.Destination{}, "LastSeenAt"),
		opt.TimeWithThreshold(5*time.Second)),
}

var cmpHTTPRequestShallow = cmp.Comparer(func(x, y *http.Request) bool {
	return x.Method == y.Method &&
		x.URL.Path == y.URL.Path &&
		reflect.DeepEqual(x.URL.Query(), y.URL.Query())
})

func readFile(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	assert.NilError(t, err)
	return string(raw)
}

type fakeKubeAPI struct {
	t          *testing.T
	writes     []*http.Request
	writesLock sync.Mutex
}

func (f *fakeKubeAPI) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	headers := w.Header()
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/apis/rbac.authorization.k8s.io/v1/clusterroles":
		roleMap := map[string][]string{
			"kubernetes.io/bootstrapping=rbac-defaults": {"admin", "view", "edit"},
			"app.infrahq.com/include-role=true":         {"custom", "logs"},
		}

		headers.Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		selector := req.URL.Query().Get("labelSelector")
		roles := roleMap[selector]

		if selector == "" {
			for _, items := range roleMap {
				roles = append(roles, items...)
			}
		}

		result := rbacv1.ClusterRoleList{}
		for _, role := range roles {
			result.Items = append(result.Items,
				rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: role}})
		}
		assert.Check(f.t, json.NewEncoder(w).Encode(result))

	case req.Method == http.MethodGet && req.URL.Path == "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings":
		headers.Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		result := rbacv1.ClusterRoleBindingList{
			Items: []rbacv1.ClusterRoleBinding{},
		}
		assert.Check(f.t, json.NewEncoder(w).Encode(result))

	case req.Method == http.MethodGet && req.URL.Path == "/apis/rbac.authorization.k8s.io/v1/rolebindings":
		headers.Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		result := rbacv1.RoleBindingList{
			Items: []rbacv1.RoleBinding{},
		}
		assert.Check(f.t, json.NewEncoder(w).Encode(result))

	case req.Method == http.MethodGet && req.URL.Path == "/api/v1/namespaces":
		headers.Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		result := corev1.NamespaceList{
			Items: []corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "ns2"}},
			},
		}
		assert.Check(f.t, json.NewEncoder(w).Encode(result))

	case req.Method == http.MethodPut && strings.HasPrefix(req.URL.Path, "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/"):
		f.writes = append(f.writes, req)
		headers.Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		result := rbacv1.ClusterRoleBinding{}
		assert.Check(f.t, json.NewEncoder(w).Encode(result))

	// /apis/rbac.authorization.k8s.io/v1/namespaces/ns1/rolebindings/
	// /apis/rbac.authorization.k8s.io/v1/clusterrolebindings/
	case req.Method == http.MethodPut && strings.HasPrefix(req.URL.Path, "/apis/rbac.authorization.k8s.io/v1/"):
		f.writesLock.Lock()
		defer f.writesLock.Unlock()
		f.writes = append(f.writes, req)
		headers.Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		result := rbacv1.ClusterRoleBinding{}
		assert.Check(f.t, json.NewEncoder(w).Encode(result))

	default:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "unexpected request to fakeKube: %v %v", req.Method, req.URL)
	}
}
