package access

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/infrahq/infra/internal"
	"github.com/infrahq/infra/internal/generate"
	"github.com/infrahq/infra/internal/logging"
	"github.com/infrahq/infra/internal/server/data"
	"github.com/infrahq/infra/internal/server/models"
	"github.com/infrahq/infra/internal/validate"
)

func CreateCredential(c *gin.Context, user models.Identity) (string, error) {
	db, err := RequireInfraRole(c, models.InfraAdminRole)
	if err != nil {
		return "", HandleAuthErr(err, "user", "create", models.InfraAdminRole)
	}

	tmpPassword, err := generate.CryptoRandom(12, generate.CharsetPassword)
	if err != nil {
		return "", fmt.Errorf("generate: %w", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(tmpPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash: %w", err)
	}

	userCredential := &models.Credential{
		IdentityID:      user.ID,
		PasswordHash:    hash,
		OneTimePassword: true,
	}

	if err := data.CreateCredential(db, userCredential); err != nil {
		return "", err
	}

	_, err = data.CreateProviderUser(db, data.InfraProvider(db), &user)
	if err != nil {
		return "", fmt.Errorf("creating provider user: %w", err)
	}

	return tmpPassword, nil
}

func UpdateCredential(c *gin.Context, user *models.Identity, oldPassword, newPassword string) error {
	rCtx := GetRequestContext(c)
	isSelf := isIdentitySelf(rCtx, data.GetIdentityOptions{ByID: user.ID})

	// anyone can update their own credentials, so check authorization when not self
	if !isSelf {
		err := IsAuthorized(rCtx, models.InfraAdminRole)
		if err != nil {
			return HandleAuthErr(err, "user", "update", models.InfraAdminRole)
		}
	}

	// Users have to supply their old password to change their existing password
	if isSelf {
		if oldPassword == "" {
			errs := make(validate.Error)
			errs["oldPassword"] = append(errs["oldPassword"], "is required")
			return errs
		}

		userCredential, err := data.GetCredentialByUserID(rCtx.DBTxn, user.ID)
		if err != nil {
			return fmt.Errorf("existing credential: %w", err)
		}

		// compare the stored hash of the user's password and the hash of the presented password
		err = bcrypt.CompareHashAndPassword(userCredential.PasswordHash, []byte(oldPassword))
		if err != nil {
			// this probably means the password was wrong
			logging.L.Trace().Err(err).Msg("bcrypt comparison with oldpassword/newpassword failed")

			errs := make(validate.Error)
			errs["oldPassword"] = append(errs["oldPassword"], "invalid oldPassword")
			return errs
		}

	}

	if err := updateCredential(c, user, newPassword, isSelf); err != nil {
		return err
	}

	if !isSelf {
		// if the request is from an admin, the infra user may not exist yet, so create the
		// provider_user if it's missing.
		_, _ = data.CreateProviderUser(rCtx.DBTxn, data.InfraProvider(rCtx.DBTxn), user)
	}

	return nil
}

func updateCredential(c *gin.Context, user *models.Identity, newPassword string, isSelf bool) error {
	rCtx := GetRequestContext(c)
	db := rCtx.DBTxn

	err := checkPasswordRequirements(user.Name, newPassword)
	if err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}

	userCredential, err := data.GetCredentialByUserID(db, user.ID)
	if err != nil {
		if errors.Is(err, internal.ErrNotFound) && !isSelf {
			if err := data.CreateCredential(db, &models.Credential{
				IdentityID:      user.ID,
				PasswordHash:    hash,
				OneTimePassword: true,
			}); err != nil {
				return fmt.Errorf("creating credentials: %w", err)
			}
			return nil
		}
		return fmt.Errorf("existing credential: %w", err)
	}

	userCredential.PasswordHash = hash
	userCredential.OneTimePassword = !isSelf

	if err := data.UpdateCredential(db, userCredential); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	if isSelf {
		// if we updated our own password, remove the password-reset scope from our access key.
		if accessKey := rCtx.Authenticated.AccessKey; accessKey != nil {
			scopes := models.CommaSeparatedStrings{}
			for _, s := range accessKey.Scopes {
				if s != models.ScopePasswordReset {
					scopes = append(scopes, s)
				}
			}

			accessKey.Scopes = scopes

			if err = data.UpdateAccessKey(db, accessKey); err != nil {
				return fmt.Errorf("updating access key: %w", err)
			}
		}
	}
	return nil
}

func GetRequestContext(c *gin.Context) RequestContext {
	if raw, ok := c.Get(RequestContextKey); ok {
		if rCtx, ok := raw.(RequestContext); ok {
			return rCtx
		}
	}
	return RequestContext{}
}

func checkPasswordRequirements(user string, password string) error {
	// minimum length
	if len(password) < 8 {
		return validate.Error{"password": []string{"must be at least 8 characters"}}
	}

	// cannot contain user name
	if strings.Contains(password, user) {
		return validate.Error{"password": []string{"cannot contain user name"}}
	}

	// cannot contain name of service
	if strings.Contains(password, "infra") {
		return validate.Error{"password": []string{"cannot contain common names such as the name of the service"}}
	}

	// common sequences
	if hasSequence(password) {
		return validate.Error{"password": []string{"must not have common sequences of characters"}}
	}

	// repeated characters
	if hasRepeat(password) {
		return validate.Error{"password": []string{"must not have repeating characters"}}
	}

	return nil
}

func hasRepeat(password string) bool {
	var char rune
	var count = 0
	for _, c := range password {
		if c != char {
			char = c
			count = 1
			continue
		}

		count++
		if count >= 4 {
			return true
		}
	}

	return false
}

func hasSequence(password string) bool {
	var char rune
	var count = 0
	for _, c := range password {
		if c != char+1 {
			char = c
			count = 1
			continue
		}

		count++
		if count >= 4 {
			return true
		}
	}

	return false
}
