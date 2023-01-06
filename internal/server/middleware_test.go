package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/opt"

	"github.com/infrahq/infra/api"
	"github.com/infrahq/infra/internal"
	"github.com/infrahq/infra/internal/access"
	"github.com/infrahq/infra/internal/generate"
	"github.com/infrahq/infra/internal/server/data"
	"github.com/infrahq/infra/internal/server/models"
	"github.com/infrahq/infra/internal/testing/database"
	tpatch "github.com/infrahq/infra/internal/testing/patch"
	"github.com/infrahq/infra/uid"
)

func setupDB(t *testing.T) *data.DB {
	t.Helper()
	tpatch.ModelsSymmetricKey(t)

	db, err := data.NewDB(data.NewDBOptions{DSN: database.PostgresDriver(t, "_server").DSN})
	assert.NilError(t, err)
	t.Cleanup(func() {
		assert.NilError(t, db.Close())
	})

	return db
}

func issueToken(t *testing.T, db data.WriteTxn, identityName string, sessionDuration time.Duration) string {
	user := &models.Identity{Name: identityName}

	err := data.CreateIdentity(db, user)
	assert.NilError(t, err)

	provider := data.InfraProvider(db)

	token := &models.AccessKey{
		IssuedFor:  user.ID,
		ProviderID: provider.ID,
		ExpiresAt:  time.Now().Add(sessionDuration).UTC(),
	}
	body, err := data.CreateAccessKey(db, token)
	assert.NilError(t, err)

	return body
}

func TestRequireAccessKey(t *testing.T) {
	type testCase struct {
		setup    func(t *testing.T, db data.WriteTxn) *http.Request
		expected func(t *testing.T, authned access.Authenticated, err error)
	}
	cases := map[string]testCase{
		"AccessKeyValid": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				authentication := issueToken(t, db, "existing@infrahq.com", time.Minute*1)
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", "Bearer "+authentication)
				return r
			},
			expected: func(t *testing.T, actual access.Authenticated, err error) {
				assert.NilError(t, err)
				assert.Equal(t, actual.User.Name, "existing@infrahq.com")
			},
		},
		"AccessKeyValidForProvider": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				provider := data.InfraProvider(db)
				token := &models.AccessKey{
					IssuedFor:  provider.ID,
					ProviderID: provider.ID,
					Name:       fmt.Sprintf("%s-scim", provider.Name),
					ExpiresAt:  time.Now().Add(1 * time.Minute).UTC(),
				}
				authentication, err := data.CreateAccessKey(db, token)
				assert.NilError(t, err)
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", "Bearer "+authentication)
				return r
			},
			expected: func(t *testing.T, actual access.Authenticated, err error) {
				assert.NilError(t, err)
				assert.Assert(t, actual.User == nil)
			},
		},
		"AccessKeyInvalidForProviderNotMatchingIssuedAndProvider": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				provider := data.InfraProvider(db)
				token := &models.AccessKey{
					IssuedFor: provider.ID,
					Name:      fmt.Sprintf("%s-scim", provider.Name),
					ExpiresAt: time.Now().Add(1 * time.Minute).UTC(),
				}
				authentication, err := data.CreateAccessKey(db, token)
				assert.NilError(t, err)
				token.ProviderID = 123
				err = data.UpdateAccessKey(db, token)
				assert.NilError(t, err)

				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", "Bearer "+authentication)
				return r
			},
			expected: func(t *testing.T, actual access.Authenticated, err error) {
				assert.ErrorContains(t, err, "identity for access key: record not found")
			},
		},
		"ValidAuthCookie": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				authentication := issueToken(t, db, "existing@infrahq.com", time.Minute*1)

				r := httptest.NewRequest(http.MethodGet, "/", nil)

				r.AddCookie(&http.Cookie{
					Name:     cookieAuthorizationName,
					Value:    authentication,
					MaxAge:   int(time.Until(time.Now().Add(time.Minute * 1)).Seconds()),
					Path:     cookiePath,
					SameSite: http.SameSiteStrictMode,
					Secure:   true,
					HttpOnly: true,
				})

				r.Header.Add("Authorization", " ")
				return r
			},
			expected: func(t *testing.T, actual access.Authenticated, err error) {
				assert.NilError(t, err)
				assert.Equal(t, actual.User.Name, "existing@infrahq.com")
			},
		},
		"ValidSignupCookie": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				authentication := issueToken(t, db, "existing@infrahq.com", time.Minute*1)

				r := httptest.NewRequest(http.MethodGet, "/", nil)

				r.AddCookie(&http.Cookie{
					Name:     cookieSignupName,
					Value:    authentication,
					MaxAge:   int(time.Until(time.Now().Add(time.Minute * 1)).Seconds()),
					Path:     cookiePath,
					SameSite: http.SameSiteStrictMode,
					Secure:   true,
					HttpOnly: true,
				})

				r.Header.Add("Authorization", " ")
				return r
			},
			expected: func(t *testing.T, actual access.Authenticated, err error) {
				assert.NilError(t, err)
				assert.Equal(t, actual.User.Name, "existing@infrahq.com")
			},
		},
		"SignupCookieIsUsedOverAuthCookie": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				authentication := issueToken(t, db, "existing@infrahq.com", time.Minute*1)

				r := httptest.NewRequest(http.MethodGet, "/", nil)

				r.AddCookie(&http.Cookie{
					Name:     cookieSignupName,
					Value:    authentication,
					MaxAge:   int(time.Until(time.Now().Add(time.Minute * 1)).Seconds()),
					Path:     cookiePath,
					SameSite: http.SameSiteStrictMode,
					Secure:   true,
					HttpOnly: true,
				})

				r.AddCookie(&http.Cookie{
					Name:     cookieSignupName,
					Value:    "invalid.access.key",
					MaxAge:   int(time.Until(time.Now().Add(time.Minute * 1)).Seconds()),
					Path:     cookiePath,
					SameSite: http.SameSiteStrictMode,
					Secure:   true,
					HttpOnly: true,
				})

				r.Header.Add("Authorization", " ")
				return r
			},
			expected: func(t *testing.T, actual access.Authenticated, err error) {
				assert.NilError(t, err)
				assert.Equal(t, actual.User.Name, "existing@infrahq.com")
			},
		},
		"AccessKeyExpired": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				authentication := issueToken(t, db, "existing@infrahq.com", time.Minute*-1)
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", "Bearer "+authentication)
				return r
			},
			expected: func(t *testing.T, _ access.Authenticated, err error) {
				assert.Error(t, err, "access key has expired")
			},
		},
		"AccessKeyInvalidKey": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				token := issueToken(t, db, "existing@infrahq.com", time.Minute*1)
				secret := token[:models.AccessKeySecretLength]
				authentication := fmt.Sprintf("%s.%s", uid.New().String(), secret)
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", "Bearer "+authentication)
				return r
			},
			expected: func(t *testing.T, _ access.Authenticated, err error) {
				assert.ErrorContains(t, err, "record not found")
			},
		},
		"AccessKeyNoMatch": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				authentication := fmt.Sprintf("%s.%s", uid.New().String(), generate.MathRandom(models.AccessKeySecretLength, generate.CharsetAlphaNumeric))
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", "Bearer "+authentication)
				return r
			},
			expected: func(t *testing.T, _ access.Authenticated, err error) {
				assert.ErrorContains(t, err, "record not found")
			},
		},
		"AccessKeyInvalidSecret": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				token := issueToken(t, db, "existing@infrahq.com", time.Minute*1)
				authentication := fmt.Sprintf("%s.%s", strings.Split(token, ".")[0], generate.MathRandom(models.AccessKeySecretLength, generate.CharsetAlphaNumeric))
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", "Bearer "+authentication)
				return r
			},
			expected: func(t *testing.T, _ access.Authenticated, err error) {
				assert.ErrorContains(t, err, "access key invalid secret")
			},
		},
		"UnknownAuthenticationMethod": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				authentication, err := generate.CryptoRandom(32, generate.CharsetAlphaNumeric)
				assert.NilError(t, err)
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", "Bearer "+authentication)
				return r
			},
			expected: func(t *testing.T, _ access.Authenticated, err error) {
				assert.ErrorContains(t, err, "invalid access key format")
			},
		},
		"NoAuthentication": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				// nil pointer if we don't seup the request header here
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				return r
			},
			expected: func(t *testing.T, _ access.Authenticated, err error) {
				assert.ErrorContains(t, err, "authentication is required")
			},
		},
		"EmptyAuthentication": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", "")
				return r
			},
			expected: func(t *testing.T, _ access.Authenticated, err error) {
				assert.ErrorContains(t, err, "authentication is required")
			},
		},
		"EmptySpaceAuthentication": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Add("Authorization", " ")
				return r
			},
			expected: func(t *testing.T, _ access.Authenticated, err error) {
				assert.ErrorContains(t, err, "authentication is required")
			},
		},
		"EmptyCookieAuthentication": {
			setup: func(t *testing.T, db data.WriteTxn) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)

				r.AddCookie(&http.Cookie{
					Name:     cookieAuthorizationName,
					MaxAge:   int(time.Until(time.Now().Add(time.Minute * 1)).Seconds()),
					Path:     cookiePath,
					SameSite: http.SameSiteStrictMode,
					Secure:   true,
					HttpOnly: true,
				})

				r.Header.Add("Authorization", " ")
				return r
			},
			expected: func(t *testing.T, _ access.Authenticated, err error) {
				assert.ErrorContains(t, err, "bearer token was missing")
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := setupDB(t)

			srv := &Server{
				options: Options{
					BaseDomain: "example.com",
				},
			}

			req := tc.setup(t, db)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = req

			tx := txnForTestCase(t, db, db.DefaultOrg.ID)
			authned, err := requireAccessKey(c, tx, srv)
			tc.expected(t, authned, err)
		})
	}
}

func TestHandleInfraDestinationHeader(t *testing.T) {
	srv := setupServer(t, withAdminUser)
	routes := srv.GenerateRoutes()
	db := srv.DB()

	connector := models.Identity{Name: "connectorA"}
	err := data.CreateIdentity(db, &connector)
	assert.NilError(t, err)

	grant := models.Grant{
		Subject:   uid.NewIdentityPolymorphicID(connector.ID),
		Privilege: models.InfraConnectorRole,
		Resource:  "infra",
	}
	err = data.CreateGrant(db, &grant)
	assert.NilError(t, err)

	token := models.AccessKey{
		IssuedFor:  connector.ID,
		ProviderID: data.InfraProvider(db).ID,
		ExpiresAt:  time.Now().Add(time.Hour).UTC(),
	}
	secret, err := data.CreateAccessKey(db, &token)
	assert.NilError(t, err)

	t.Run("with uniqueID", func(t *testing.T) {
		destination := &models.Destination{
			Name:     t.Name(),
			UniqueID: "the-unique-id",
			Kind:     models.DestinationKindKubernetes,
		}
		err := data.CreateDestination(db, destination)
		assert.NilError(t, err)

		doRequest := func() {
			r := httptest.NewRequest("GET", "/api/grants", nil)
			r.Header.Set("Infra-Version", apiVersionLatest)
			r.Header.Set(headerInfraDestinationUniqueID, destination.UniqueID)
			r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", secret))
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, r)
		}

		doRequest()
		destination, err = data.GetDestination(db, data.GetDestinationOptions{ByUniqueID: destination.UniqueID})
		assert.NilError(t, err)
		assert.DeepEqual(t, destination.LastSeenAt, time.Now(), opt.TimeWithThreshold(time.Second))

		// check LastSeenAt updates are throttled
		time.Sleep(20 * time.Millisecond)
		doRequest()
		updated, err := data.GetDestination(db, data.GetDestinationOptions{ByUniqueID: destination.UniqueID})
		assert.NilError(t, err)
		assert.Equal(t, destination.LastSeenAt, updated.LastSeenAt,
			"expected no updated to LastSeenAt")
	})

	t.Run("with name", func(t *testing.T) {
		destination := &models.Destination{
			Name: "the-destination-name",
			Kind: models.DestinationKindKubernetes,
		}
		err := data.CreateDestination(db, destination)
		assert.NilError(t, err)

		doRequest := func() {
			r := httptest.NewRequest("GET", "/api/grants", nil)
			r.Header.Set("Infra-Version", apiVersionLatest)
			r.Header.Set(headerInfraDestinationName, destination.Name)
			r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", secret))
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, r)
		}

		doRequest()
		destination, err = data.GetDestination(db, data.GetDestinationOptions{ByName: destination.Name})
		assert.NilError(t, err)
		assert.DeepEqual(t, destination.LastSeenAt, time.Now(), opt.TimeWithThreshold(time.Second))
	})

	t.Run("good no destination header", func(t *testing.T) {
		destination := &models.Destination{
			Name:     t.Name(),
			UniqueID: t.Name(),
			Kind:     models.DestinationKindKubernetes,
		}
		err := data.CreateDestination(db, destination)
		assert.NilError(t, err)

		r := httptest.NewRequest("GET", "/good", nil)
		r.Header.Add("Authorization", fmt.Sprintf("Bearer %s", secret))
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)

		destination, err = data.GetDestination(db, data.GetDestinationOptions{ByUniqueID: destination.UniqueID})
		assert.NilError(t, err)
		assert.Equal(t, destination.LastSeenAt.UTC(), time.Time{})
	})

	t.Run("good no destination", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/good", nil)
		r.Header.Add("Infra-Destination", "nonexistent")
		r.Header.Add("Authorization", fmt.Sprintf("Bearer %s", secret))
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)

		_, err := data.GetDestination(db, data.GetDestinationOptions{ByUniqueID: "nonexistent"})
		assert.ErrorIs(t, err, internal.ErrNotFound)
	})
}

func TestAuthenticateRequest(t *testing.T) {
	srv := setupServer(t, withAdminUser)
	routes := srv.GenerateRoutes()

	org := &models.Organization{
		Name:   "The Umbrella Academy",
		Domain: "umbrella.infrahq.com",
	}
	otherOrg := &models.Organization{
		Name:   "The Factory",
		Domain: "the-factory-xyz8.infrahq.com",
	}
	createOrgs(t, srv.db, otherOrg, org)

	tx, err := srv.db.Begin(context.Background(), nil)
	assert.NilError(t, err)
	tx = tx.WithOrgID(org.ID)

	user := &models.Identity{
		Name:               "userone@example.com",
		OrganizationMember: models.OrganizationMember{OrganizationID: org.ID},
	}
	createIdentities(t, tx, user)

	token := &models.AccessKey{
		IssuedFor:           user.ID,
		ProviderID:          data.InfraProvider(tx).ID,
		ExpiresAt:           time.Now().Add(10 * time.Second),
		InactivityExtension: time.Hour,
		OrganizationMember:  models.OrganizationMember{OrganizationID: org.ID},
	}

	key, err := data.CreateAccessKey(tx, token)
	assert.NilError(t, err)

	assert.NilError(t, tx.Commit())

	httpSrv := httptest.NewServer(routes)
	t.Cleanup(httpSrv.Close)

	type testCase struct {
		name     string
		setup    func(t *testing.T, req *http.Request)
		expected func(t *testing.T, resp *http.Response)
	}

	var now time.Time

	run := func(t *testing.T, tc testCase) {
		// Any authenticated route will do
		routeURL := httpSrv.URL + "/api/users/" + user.ID.String()

		// nolint:noctx
		req, err := http.NewRequest("GET", routeURL, nil)
		assert.NilError(t, err)
		req.Header.Set("Infra-Version", apiVersionLatest)

		if tc.setup != nil {
			tc.setup(t, req)
		}

		client := httpSrv.Client()
		resp, err := client.Do(req)
		assert.NilError(t, err)

		tc.expected(t, resp)
	}

	testCases := []testCase{
		{
			name: "Org ID from access key",
			setup: func(t *testing.T, req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+key)
			},
			expected: func(t *testing.T, resp *http.Response) {
				body, err := io.ReadAll(resp.Body)
				assert.NilError(t, err)

				assert.Equal(t, resp.StatusCode, http.StatusOK, string(body))

				respUser := &api.User{}
				assert.NilError(t, json.Unmarshal(body, respUser))
				assert.Equal(t, respUser.ID, user.ID)
			},
		},
		{
			name: "Missing access key",
			setup: func(t *testing.T, req *http.Request) {
				req.Host = org.Domain
			},
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusUnauthorized)
			},
		},
		{
			name: "Org ID from access key and hostname match",
			setup: func(t *testing.T, req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+key)
				req.Host = org.Domain
			},
			expected: func(t *testing.T, resp *http.Response) {
				body, err := io.ReadAll(resp.Body)
				assert.NilError(t, err)

				assert.Equal(t, resp.StatusCode, http.StatusOK, string(body))

				respUser := &api.User{}
				assert.NilError(t, json.Unmarshal(body, respUser))
				assert.Equal(t, respUser.ID, user.ID)
			},
		},
		{
			name: "Org ID from access key and hostname conflict",
			setup: func(t *testing.T, req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+key)
				req.Host = otherOrg.Domain
			},
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusBadRequest)
			},
		},
		{
			name: "LastSeenAt and InactivityTimeout are updated",
			setup: func(t *testing.T, req *http.Request) {
				tx := txnForTestCase(t, srv.db, org.ID)
				user := *user // shallow copy user
				user.LastSeenAt = time.Date(2022, 1, 2, 3, 4, 5, 0, time.UTC)
				assert.NilError(t, data.UpdateIdentity(tx, &user))

				ak := *token // shallow copy access key
				ak.InactivityTimeout = time.Now().Add(2 * time.Minute)
				assert.NilError(t, data.UpdateAccessKey(tx, &ak))

				assert.NilError(t, tx.Commit())

				req.Header.Set("Authorization", "Bearer "+key)
			},
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusOK, resp)

				tx := txnForTestCase(t, srv.db, org.ID)
				user, err := data.GetIdentity(tx, data.GetIdentityOptions{ByID: user.ID})
				assert.NilError(t, err)
				assert.DeepEqual(t, user.LastSeenAt, time.Now(), opt.TimeWithThreshold(time.Second))

				ak, err := data.GetAccessKey(tx, data.GetAccessKeysOptions{ByID: token.ID})
				assert.NilError(t, err)
				assert.DeepEqual(t, ak.InactivityTimeout, time.Now().Add(token.InactivityExtension),
					opt.TimeWithThreshold(time.Second))
			},
		},
		{
			name: "LastSeenAt and InactivityTimeout updates are throttled",
			setup: func(t *testing.T, req *http.Request) {
				now = time.Now().Truncate(time.Microsecond) // truncate to DB precision

				tx := txnForTestCase(t, srv.db, org.ID)
				user := *user // shallow copy user
				user.LastSeenAt = now
				assert.NilError(t, data.UpdateIdentity(tx, &user))

				ak := *token // shallow copy access key
				ak.InactivityTimeout = now.Add(ak.InactivityExtension)
				assert.NilError(t, data.UpdateAccessKey(tx, &ak))

				assert.NilError(t, tx.Commit())

				time.Sleep(20 * time.Millisecond)
				req.Header.Set("Authorization", "Bearer "+key)
			},
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusOK, resp)

				tx := txnForTestCase(t, srv.db, org.ID)
				user, err := data.GetIdentity(tx, data.GetIdentityOptions{ByID: user.ID})
				assert.NilError(t, err)
				assert.Equal(t, user.LastSeenAt, now, "expected no update")

				ak, err := data.GetAccessKey(tx, data.GetAccessKeysOptions{ByID: token.ID})
				assert.NilError(t, err)
				assert.Equal(t, ak.InactivityTimeout, now.Add(token.InactivityExtension),
					"expected no update")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			run(t, tc)
		})
	}
}

func TestAuthenticateRequest_HighConcurrency(t *testing.T) {
	srv := setupServer(t, withAdminUser)
	routes := srv.GenerateRoutes()

	accessKey := adminAccessKey(srv)

	httpSrv := httptest.NewServer(routes)
	t.Cleanup(httpSrv.Close)

	ctx := context.Background()
	group, ctx := errgroup.WithContext(ctx)

	total := 100
	elapsed := make([]time.Duration, total)
	chStart := make(chan struct{})
	for i := 0; i < total; i++ {
		i := i
		group.Go(func() error {
			<-chStart

			// Any authenticated route will do
			routeURL := httpSrv.URL + "/api/users/self"
			req, err := http.NewRequestWithContext(ctx, "GET", routeURL, nil)
			if err != nil {
				return err
			}
			req.Header.Set("Infra-Version", apiVersionLatest)
			req.Header.Set("Authorization", "Bearer "+accessKey)

			client := httpSrv.Client()

			before := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			elapsed[i] = time.Since(before)

			assert.Check(t, resp.StatusCode == http.StatusOK, "code=%v", resp.StatusCode)
			return nil
		})
	}
	close(chStart)
	assert.NilError(t, group.Wait())

	sort.Slice(elapsed, func(i, j int) bool {
		return elapsed[i] > elapsed[j]
	})

	assert.Assert(t, elapsed[0] < time.Second, "times=%v", elapsed)
}

func TestValidateRequestOrganization(t *testing.T) {
	srv := setupServer(t, withAdminUser)
	srv.options.EnableSignup = true // multi-tenant environment
	srv.options.BaseDomain = "example.com"
	routes := srv.GenerateRoutes()

	org := &models.Organization{
		Name:   "The Umbrella Academy",
		Domain: "umbrella.infrahq.com",
	}
	otherOrg := &models.Organization{
		Name:   "The Factory",
		Domain: "the-factory-xyz8.infrahq.com",
	}
	createOrgs(t, srv.db, otherOrg, org)

	tx, err := srv.db.Begin(context.Background(), nil)
	assert.NilError(t, err)
	tx = tx.WithOrgID(org.ID)

	provider := &models.Provider{
		Name:               "electric",
		Kind:               models.ProviderKindGoogle,
		OrganizationMember: models.OrganizationMember{OrganizationID: org.ID},
	}
	assert.NilError(t, data.CreateProvider(tx, provider))

	user := &models.Identity{
		Name:               "userone@example.com",
		OrganizationMember: models.OrganizationMember{OrganizationID: org.ID},
	}
	createIdentities(t, tx, user)

	token := &models.AccessKey{
		IssuedFor:          user.ID,
		ProviderID:         data.InfraProvider(tx).ID,
		ExpiresAt:          time.Now().Add(10 * time.Second),
		OrganizationMember: models.OrganizationMember{OrganizationID: org.ID},
	}

	key, err := data.CreateAccessKey(tx, token)
	assert.NilError(t, err)

	assert.NilError(t, tx.Commit())

	httpSrv := httptest.NewServer(routes)
	t.Cleanup(httpSrv.Close)

	type testCase struct {
		name     string
		route    string
		setup    func(t *testing.T, req *http.Request)
		expected func(t *testing.T, resp *http.Response)
	}

	run := func(t *testing.T, tc testCase) {
		// Any unauthenticated route will do
		routeURL := httpSrv.URL + "/api/providers"
		if tc.route != "" {
			routeURL = httpSrv.URL + tc.route
		}

		// nolint:noctx
		req, err := http.NewRequest("GET", routeURL, nil)
		assert.NilError(t, err)
		req.Header.Set("Infra-Version", apiVersionLatest)

		if tc.setup != nil {
			tc.setup(t, req)
		}

		client := httpSrv.Client()
		resp, err := client.Do(req)
		assert.NilError(t, err)

		tc.expected(t, resp)
	}

	expectSuccess := func(t *testing.T, resp *http.Response) {
		body, err := io.ReadAll(resp.Body)
		assert.NilError(t, err)

		assert.Equal(t, resp.StatusCode, http.StatusOK, string(body))

		respProviders := &api.ListResponse[api.Provider]{}
		assert.NilError(t, json.Unmarshal(body, respProviders))
		assert.Equal(t, len(respProviders.Items), 1)
		assert.Equal(t, respProviders.Items[0].ID, provider.ID)
	}

	testCases := []testCase{
		{
			name: "Org ID from access key",
			setup: func(t *testing.T, req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+key)
			},
			expected: expectSuccess,
		},
		{
			name: "Org ID from hostname",
			setup: func(t *testing.T, req *http.Request) {
				req.Host = org.Domain
			},
			expected: expectSuccess,
		},
		{
			name: "Org ID from access key and hostname match",
			setup: func(t *testing.T, req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+key)
				req.Host = org.Domain
			},
			expected: expectSuccess,
		},
		{
			name: "Org ID from access key and hostname conflict",
			setup: func(t *testing.T, req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+key)
				req.Host = otherOrg.Domain
			},
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusBadRequest)
			},
		},
		{
			name: "missing org with single-tenancy returns default",
			setup: func(t *testing.T, req *http.Request) {
				srv.options.EnableSignup = false
				t.Cleanup(func() {
					srv.options.EnableSignup = true
				})
			},
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusOK)
			},
		},
		{
			name:  "missing org with multi-tenancy, route ignores org",
			route: "/api/version",
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusOK)
			},
		},
		{
			name: "missing org with multi-tenancy, route returns error",
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusBadRequest)
			},
		},
		{
			name: "missing org with multi-tenancy, route returns fake data",
			setup: func(t *testing.T, req *http.Request) {
				t.Skip("TODO: not yet implemented")
			},
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusOK)
				// TODO: check fake data
			},
		},
		{
			name:  "unknown hostname works like missing org",
			route: "/api/version",
			setup: func(t *testing.T, req *http.Request) {
				req.Host = "http://notadomainweknowabout.org/foo"
			},
			expected: func(t *testing.T, resp *http.Response) {
				assert.Equal(t, resp.StatusCode, http.StatusOK)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			run(t, tc)
		})
	}
}
