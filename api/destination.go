package api

import (
	"github.com/infrahq/infra/internal/validate"
	"github.com/infrahq/infra/uid"
)

type Destination struct {
	ID         uid.ID                `json:"id" note:"ID of the destination" example:"7a1b26b33F"`
	UniqueID   string                `json:"uniqueID" form:"uniqueID" note:"Unique ID generated by the connector" example:"94c2c570a20311180ec325fd56"`
	Name       string                `json:"name" form:"name" note:"Name of the destination" example:"production-cluster"`
	Kind       string                `json:"kind" note:"Kind of destination. eg. kubernetes or ssh or postgres" example:"kubernetes"`
	Created    Time                  `json:"created" note:"Time destination was created" example:"2022-11-10T23:35:22Z"`
	Updated    Time                  `json:"updated" note:"Time destination was updated" example:"2022-12-01T19:48:55Z"`
	Connection DestinationConnection `json:"connection" note:"Object that includes the URL and CA for the destination"`

	Resources []string `json:"resources" note:"Destination specific. For Kubernetes, it is the list of namespaces" example:"['default', 'kube-system']"`
	Roles     []string `json:"roles" example:"['cluster-admin', 'admin', 'edit', 'view', 'exec', 'logs', 'port-forward']" note:"Destination specific. For Kubernetes, it is the list of cluster roles available on that cluster"`

	LastSeen  Time `json:"lastSeen"`
	Connected bool `json:"connected" note:"Shows if the destination is currently connected" example:"true"`

	Version string `json:"version" note:"Application version of the connector for this destination"`
}

type DestinationConnection struct {
	// TODO: URL is not a full url, it's set to a host:port by the connector
	URL string `json:"url" example:"aa60eexample.us-west-2.elb.amazonaws.com"`
	CA  PEM    `json:"ca" example:"-----BEGIN CERTIFICATE-----\nMIIDNTCCAh2gAwIBAgIRALRetnpcTo9O3V2fAK3ix+c\n-----END CERTIFICATE-----\n"`
}

func (r DestinationConnection) ValidationRules() []validate.ValidationRule {
	return []validate.ValidationRule{
		validate.Required("ca", r.CA),
	}
}

type ListDestinationsRequest struct {
	Name     string `form:"name" note:"Name of the destination" example:"production-cluster"`
	Kind     string `form:"kind" note:"Kind of destination. eg. kubernetes or ssh or postgres" example:"kubernetes"`
	UniqueID string `form:"unique_id" note:"Unique ID generated by the connector" example:"94c2c570a20311180ec325fd56"`
	PaginationRequest
}

func (r ListDestinationsRequest) ValidationRules() []validate.ValidationRule {
	// no-op ValidationRules implementation so that the rules from the
	// embedded PaginationRequest struct are not applied twice.
	return nil
}

type CreateDestinationRequest struct {
	UniqueID   string                `json:"uniqueID" note:"Unique ID used to identify this specific destination" example:"94c2c570a20311180ec325fd56"`
	Name       string                `json:"name" note:"Name of the destination" example:"production-cluster"`
	Kind       string                `json:"kind" note:"Kind of destination. eg. kubernetes or ssh or postgres" example:"kubernetes"`
	Version    string                `json:"version" note:"Application version of the connector for this destination"`
	Connection DestinationConnection `json:"connection" note:"Object that includes the URL and CA for the destination"`

	Resources []string `json:"resources"`
	Roles     []string `json:"roles"`
}

func (r CreateDestinationRequest) ValidationRules() []validate.ValidationRule {
	return []validate.ValidationRule{
		validateDestinationName(r.Name),
		validate.Required("name", r.Name),
		validate.ReservedStrings("name", r.Name, []string{"infra"}),

		// Allow "" for versions 0.16.1 and prior
		// TODO: make this required in the future
		validate.Enum("kind", r.Kind, []string{"kubernetes", "ssh", ""}),
	}
}

type UpdateDestinationRequest struct {
	ID         uid.ID                `uri:"id" json:"-" note:"ID of the destination" example:"7a1b26b33F"`
	Name       string                `json:"name" note:"Name of the destination" example:"production-cluster"`
	UniqueID   string                `json:"uniqueID" note:"Unique ID generated by the connector" example:"94c2c570a20311180ec325fd56"`
	Version    string                `json:"version" note:"Application version of the connector for this destination"`
	Connection DestinationConnection `json:"connection" note:"Object that includes the URL and CA for the destination"`

	Resources []string `json:"resources"`
	Roles     []string `json:"roles"`
}

func (r UpdateDestinationRequest) ValidationRules() []validate.ValidationRule {
	return []validate.ValidationRule{
		validate.Required("id", r.ID),
		validate.Required("name", r.Name),
		validateDestinationName(r.Name),
	}
}

func (req ListDestinationsRequest) SetPage(page int) Paginatable {
	req.PaginationRequest.Page = page

	return req
}

func validateDestinationName(value string) validate.StringRule {
	rule := ValidateName(value)
	// dots are not allowed in destination name, because it would make grants
	// ambiguous. We use dots to separate destination name from resource name in
	// the Grant.Resource field.
	rule.CharacterRanges = []validate.CharRange{
		validate.AlphabetLower,
		validate.AlphabetUpper,
		validate.Numbers,
		validate.Dash, validate.Underscore,
	}
	return rule
}

type ListDestinationAccessRequest struct {
	Name string `uri:"id"` // TODO: change to ID when grants stores destinationID
	BlockingRequest
}

type ListDestinationAccessResponse struct {
	Items           []DestinationAccess `json:"items"`
	LastUpdateIndex `json:"-"`
}

type DestinationAccess struct {
	UserID           uid.ID `json:"userID"`
	UserSSHLoginName string `json:"userSSHLoginName"`
	Privilege        string `json:"privilege"`
	Resource         string `json:"resource"`
}
