package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/infrahq/infra/api"
	"github.com/infrahq/infra/internal"
	"github.com/infrahq/infra/internal/access"
	"github.com/infrahq/infra/internal/logging"
	"github.com/infrahq/infra/internal/server/data"
	"github.com/infrahq/infra/internal/server/models"
	"github.com/infrahq/infra/uid"
)

func (a *API) ListDestinations(c *gin.Context, r *api.ListDestinationsRequest) (*api.ListResponse[api.Destination], error) {
	rCtx := getRequestContext(c)
	p := PaginationFromRequest(r.PaginationRequest)

	opts := data.ListDestinationsOptions{
		ByUniqueID: r.UniqueID,
		ByName:     r.Name,
		ByKind:     r.Kind,
		Pagination: &p,
	}
	destinations, err := data.ListDestinations(rCtx.DBTxn, opts)
	if err != nil {
		return nil, err
	}

	result := api.NewListResponse(destinations, PaginationToResponse(p), func(destination models.Destination) api.Destination {
		return *destination.ToAPI()
	})

	return result, nil
}

func (a *API) GetDestination(c *gin.Context, r *api.Resource) (*api.Destination, error) {
	// No authorization required to view a destination
	rCtx := getRequestContext(c)
	destination, err := data.GetDestination(rCtx.DBTxn, data.GetDestinationOptions{ByID: r.ID})
	if err != nil {
		return nil, err
	}

	return destination.ToAPI(), nil
}

func (a *API) CreateDestination(c *gin.Context, r *api.CreateDestinationRequest) (*api.Destination, error) {
	rCtx := getRequestContext(c)
	destination := &models.Destination{
		Name:          r.Name,
		UniqueID:      r.UniqueID,
		Kind:          models.DestinationKind(r.Kind),
		ConnectionURL: r.Connection.URL,
		ConnectionCA:  string(r.Connection.CA),
		Resources:     r.Resources,
		Roles:         r.Roles,
		Version:       r.Version,
	}

	if destination.Kind == "" {
		destination.Kind = "kubernetes"
	}

	// set LastSeenAt if this request came from a connector. The middleware
	// can't do this update in the case where the destination did not exist yet
	switch {
	case rCtx.Request.Header.Get(headerInfraDestinationName) == r.Name:
		destination.LastSeenAt = time.Now()
	case rCtx.Request.Header.Get(headerInfraDestinationUniqueID) == r.UniqueID:
		destination.LastSeenAt = time.Now()
	}

	err := access.CreateDestination(rCtx, destination)
	if err != nil {
		return nil, fmt.Errorf("create destination: %w", err)
	}

	return destination.ToAPI(), nil
}

func (a *API) UpdateDestination(c *gin.Context, r *api.UpdateDestinationRequest) (*api.Destination, error) {
	rCtx := getRequestContext(c)

	// Start with the existing value, so that non-update fields are not set to zero.
	destination, err := data.GetDestination(rCtx.DBTxn, data.GetDestinationOptions{ByID: r.ID})
	if err != nil {
		return nil, err
	}

	destination.Name = r.Name
	destination.UniqueID = r.UniqueID
	destination.ConnectionURL = r.Connection.URL
	destination.ConnectionCA = string(r.Connection.CA)
	destination.Resources = r.Resources
	destination.Roles = r.Roles
	destination.Version = r.Version

	if err := access.UpdateDestination(rCtx, destination); err != nil {
		return nil, fmt.Errorf("update destination: %w", err)
	}

	return destination.ToAPI(), nil
}

func (a *API) DeleteDestination(c *gin.Context, r *api.Resource) (*api.EmptyResponse, error) {
	return nil, access.DeleteDestination(getRequestContext(c), r.ID)
}

// TODO: move types to api package
type ListDestinationAccessRequest struct {
	Name string `uri:"name"` // TODO: change to ID when grants stores destinationID
	api.BlockingRequest
}

type ListDestinationAccessResponse struct {
	Items               []DestinationAccess
	api.LastUpdateIndex `json:"-"`
}

type DestinationAccess struct {
	UserID           uid.ID
	UserSSHLoginName string
	Privilege        string
	Resource         string
}

func ListDestinationAccess(c *gin.Context, r *ListDestinationAccessRequest) (*ListDestinationAccessResponse, error) {
	rCtx := getRequestContext(c)
	rCtx.Response.AddLogFields(func(event *zerolog.Event) {
		event.Int64("lastUpdateIndex", r.LastUpdateIndex)
	})

	roles := []string{models.InfraAdminRole, models.InfraViewRole, models.InfraConnectorRole}
	if err := access.IsAuthorized(rCtx, roles...); err != nil {
		return nil, access.HandleAuthErr(err, "grants", "list", roles...)
	}

	if r.LastUpdateIndex == 0 {
		result, err := data.ListDestinationAccess(rCtx.DBTxn, r.Name)
		if err != nil {
			return nil, err
		}
		return &ListDestinationAccessResponse{
			Items: destinationAccessToAPI(result),
		}, nil
	}

	dest, err := data.GetDestination(rCtx.DBTxn, data.GetDestinationOptions{ByName: r.Name})
	if err != nil {
		return nil, err
	}

	grants, err := data.ListGrants(rCtx.DBTxn, data.ListGrantsOptions{ByDestination: r.Name})
	if err != nil {
		return nil, err
	}

	// Close the request scoped txn to avoid long-running transactions.
	if err := rCtx.DBTxn.Rollback(); err != nil {
		return nil, err
	}

	channels := []data.ListenChannelDescriptor{
		data.ListenChannelGrantsByDestination{
			OrgID:         rCtx.DBTxn.OrganizationID(),
			DestinationID: dest.ID,
		},
	}
	for _, grant := range grants {
		if grant.Subject.Kind == models.SubjectKindGroup {
			channels = append(channels, data.ListenChannelGroupMembership{
				OrgID:   rCtx.DBTxn.OrganizationID(),
				GroupID: grant.Subject.ID,
			})
		}
	}

	listener, err := data.ListenForNotify(rCtx.Request.Context(), rCtx.DataDB, channels...)
	if err != nil {
		return nil, fmt.Errorf("listen for notify: %w", err)
	}
	defer func() {
		// use a context with a separate deadline so that we still release
		// when the request timeout is reached
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := listener.Release(ctx); err != nil {
			logging.L.Error().Err(err).Msg("failed to release listener conn")
		}
	}()

	result, err := listDestinationAccessWithMaxUpdateIndex(rCtx, r.Name)
	if err != nil {
		return result, err
	}

	// The query returned results that are new to the client
	if result.LastUpdateIndex.Index > r.LastUpdateIndex {
		return result, nil
	}

	err = listener.WaitForNotification(rCtx.Request.Context())
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return result, internal.ErrNotModified
	case err != nil:
		return result, fmt.Errorf("waiting for notify: %w", err)
	}

	result, err = listDestinationAccessWithMaxUpdateIndex(rCtx, r.Name)
	if err != nil {
		return result, err
	}

	// TODO: also report this when we return above
	rCtx.Response.AddLogFields(func(event *zerolog.Event) {
		event.Int("numItems", len(result.Items))
	})

	return result, nil
}

func destinationAccessToAPI(a []data.DestinationAccess) []DestinationAccess {
	result := make([]DestinationAccess, 0, len(a))
	for _, item := range a {
		result = append(result, DestinationAccess{
			UserID:           item.UserID,
			UserSSHLoginName: item.UserSSHLoginName,
			Privilege:        item.Privilege,
			Resource:         item.Resource,
		})
	}
	return result
}

func listDestinationAccessWithMaxUpdateIndex(rCtx access.RequestContext, name string) (*ListDestinationAccessResponse, error) {
	tx, err := rCtx.DataDB.Begin(rCtx.Request.Context(), &sql.TxOptions{
		ReadOnly:  true,
		Isolation: sql.LevelRepeatableRead,
	})
	if err != nil {
		return nil, err
	}
	defer logError(tx.Rollback, "failed to rollback transaction")
	tx = tx.WithOrgID(rCtx.DBTxn.OrganizationID())

	result, err := data.ListDestinationAccess(tx, name)
	if err != nil {
		return nil, err
	}

	maxUpdateIndex, err := data.DestinationAccessMaxUpdateIndex(tx, name)
	if err != nil {
		return nil, err
	}
	return &ListDestinationAccessResponse{
		Items:           destinationAccessToAPI(result),
		LastUpdateIndex: api.LastUpdateIndex{Index: maxUpdateIndex},
	}, nil
}
