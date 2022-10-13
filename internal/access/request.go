package access

import (
	"net/http"

	"github.com/infrahq/infra/internal/server/data"
	"github.com/infrahq/infra/internal/server/models"
	"github.com/infrahq/infra/uid"
)

const RequestContextKey = "requestContext"

// RequestContext stores the http.Request, and values derived from the request
// like the authenticated user. It also provides a database transaction.
type RequestContext struct {
	Request       *http.Request
	DBTxn         *data.Transaction
	Authenticated Authenticated
}

// Authenticated stores data about the authenticated user. If the AccessKey or
// User are nil, it indicates that no user was authenticated.
type Authenticated struct {
	AccessKey    *models.AccessKey
	User         *models.Identity
	Organization *models.Organization
}

func (n Authenticated) OrganizationID() uid.ID {
	if org := n.Organization; org != nil {
		return org.ID
	}
	return 0
}
