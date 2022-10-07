package access

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/infrahq/infra/internal/server/data"
	"github.com/infrahq/infra/internal/server/models"
)

func ListProviderUsers(c *gin.Context, p *data.SCIMParameters) ([]models.ProviderUser, error) {
	// this can only be run by an access key issued for an identity provider
	ctx := GetRequestContext(c)
	users, err := data.ListProviderUsers(ctx.DBTxn, ctx.Authenticated.AccessKey.IssuedFor, p)
	if err != nil {
		return nil, fmt.Errorf("list provider users: %w", err)
	}
	return users, nil
}

func ProvisionProviderUser(c *gin.Context, u *models.ProviderUser) error {
	// this can only be run by an access key issued for an identity provider
	ctx := GetRequestContext(c)
	u.ProviderID = ctx.Authenticated.AccessKey.IssuedFor
	err := data.ProvisionProviderUser(ctx.DBTxn, u)
	if err != nil {
		return fmt.Errorf("provision provider user: %w", err)
	}
	return nil
}
