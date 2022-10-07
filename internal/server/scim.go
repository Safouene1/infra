package server

import (
	"github.com/gin-gonic/gin"

	"github.com/infrahq/infra/api"
	"github.com/infrahq/infra/internal/access"
	"github.com/infrahq/infra/internal/server/data"
	"github.com/infrahq/infra/internal/server/models"
)

var listProviderUsersRoute = route[api.SCIMParametersRequest, *api.ListProviderUsersResponse]{
	handler: ListProviderUsers,
	routeSettings: routeSettings{
		omitFromTelemetry:          true,
		omitFromDocs:               true,
		infraVersionHeaderOptional: true,
	},
}

var provisionProviderUserRoute = route[api.ProvisionSCIMUserRequest, *api.SCIMUser]{
	handler: ProvisionProviderUser,
	routeSettings: routeSettings{
		omitFromTelemetry:          true,
		omitFromDocs:               true,
		infraVersionHeaderOptional: true,
	},
}

func ListProviderUsers(c *gin.Context, r *api.SCIMParametersRequest) (*api.ListProviderUsersResponse, error) {
	p := data.SCIMParameters{
		StartIndex: r.StartIndex,
		Count:      r.Count,
	}
	users, err := access.ListProviderUsers(c, &p)
	if err != nil {
		return nil, err
	}
	result := &api.ListProviderUsersResponse{
		Schemas:      []string{api.ListResponseSchema},
		TotalResults: p.TotalCount,
		StartIndex:   p.StartIndex,
		ItemsPerPage: p.Count,
	}
	for _, user := range users {
		result.Resources = append(result.Resources, *user.ToAPI())
	}
	return result, nil
}

func ProvisionProviderUser(c *gin.Context, r *api.ProvisionSCIMUserRequest) (*api.SCIMUser, error) {
	user := &models.ProviderUser{
		Email:      r.UserName,
		GivenName:  r.Name.GivenName,
		FamilyName: r.Name.FamilyName,
	}
	err := access.ProvisionProviderUser(c, user)
	if err != nil {
		return nil, err
	}
	return user.ToAPI(), nil
}
