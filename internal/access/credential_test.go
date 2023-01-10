package access

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/infrahq/infra/internal/server/data"
	"github.com/infrahq/infra/internal/server/models"
)

func TestCreateCredential(t *testing.T) {
	c, db, _ := setupAccessTestContext(t)

	username := "bruce@example.com"
	user := &models.Identity{Name: username}
	err := data.CreateIdentity(db, user)
	assert.NilError(t, err)

	oneTimePassword, err := CreateCredential(c, *user)
	assert.NilError(t, err)
	assert.Assert(t, oneTimePassword != "")

	_, err = data.GetCredentialByUserID(db, user.ID)
	assert.NilError(t, err)
}

func TestUpdateCredentials(t *testing.T) {
	c, db, _ := setupAccessTestContext(t)

	username := "bruce@example.com"
	user := &models.Identity{Name: username}

	err := data.CreateIdentity(db, user)
	assert.NilError(t, err)

	tmpPassword, err := CreateCredential(c, *user)
	assert.NilError(t, err)

	userCreds, err := data.GetCredentialByUserID(db, user.ID)
	assert.NilError(t, err)

	t.Run("Update user credentials IS single use password", func(t *testing.T) {
		err := UpdateCredential(c, user, "", "newPassword")
		assert.NilError(t, err)

		creds, err := data.GetCredentialByUserID(db, user.ID)
		assert.NilError(t, err)
		assert.Equal(t, creds.OneTimePassword, true)
	})

	t.Run("Update own credentials is NOT single use password", func(t *testing.T) {
		err := data.UpdateCredential(db, userCreds)
		assert.NilError(t, err)

		rCtx := GetRequestContext(c)
		rCtx.Authenticated.User = user
		c.Set(RequestContextKey, rCtx)

		err = UpdateCredential(c, user, tmpPassword, "newPassword")
		assert.NilError(t, err)

		creds, err := data.GetCredentialByUserID(db, user.ID)
		assert.NilError(t, err)
		assert.Equal(t, creds.OneTimePassword, false)
	})

	t.Run("Update own credentials removes password reset scope, but keeps other scopes", func(t *testing.T) {
		err := data.UpdateCredential(db, userCreds)
		assert.NilError(t, err)

		rCtx := GetRequestContext(c)
		rCtx.Authenticated.User = user

		key := &models.AccessKey{
			IssuedFor:  user.ID,
			ProviderID: data.InfraProvider(db).ID,
			Scopes: []string{
				models.ScopeAllowCreateAccessKey,
				models.ScopePasswordReset,
			},
		}
		_, err = CreateAccessKey(c, key)
		assert.NilError(t, err)
		rCtx.Authenticated.AccessKey = key
		c.Set(RequestContextKey, rCtx)

		err = UpdateCredential(c, user, "", "newPassword")
		assert.ErrorContains(t, err, "oldPassword: is required")

		err = UpdateCredential(c, user, "somePassword", "newPassword")
		assert.ErrorContains(t, err, "oldPassword: invalid oldPassword")

		err = UpdateCredential(c, user, tmpPassword, "newPassword")
		assert.NilError(t, err)

		creds, err := data.GetCredentialByUserID(db, user.ID)
		assert.NilError(t, err)
		assert.Equal(t, creds.OneTimePassword, false)

		updatedKey, err := data.GetAccessKeyByKeyID(db, key.KeyID)
		assert.NilError(t, err)
		assert.DeepEqual(t, updatedKey.Scopes, models.CommaSeparatedStrings{models.ScopeAllowCreateAccessKey})
	})
}

func TestCheckPasswordRequirements(t *testing.T) {

}
