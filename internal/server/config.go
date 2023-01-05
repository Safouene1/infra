package server

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/infrahq/secrets"
	"golang.org/x/crypto/bcrypt"

	"github.com/infrahq/infra/internal"
	"github.com/infrahq/infra/internal/access"
	"github.com/infrahq/infra/internal/logging"
	"github.com/infrahq/infra/internal/server/data"
	"github.com/infrahq/infra/internal/server/models"
	"github.com/infrahq/infra/internal/validate"
	"github.com/infrahq/infra/uid"
)

type BootstrapConfig struct {
	DefaultOrganizationDomain string
	Users                     []User
}

type User struct {
	Name      string
	AccessKey string
	Password  string
	InfraRole string
}

func (u User) ValidationRules() []validate.ValidationRule {
	return []validate.ValidationRule{
		validate.Required("name", u.Name),
	}
}

func (c BootstrapConfig) ValidationRules() []validate.ValidationRule {
	// no-op implement to satisfy the interface
	return nil
}

type KeyProvider struct {
	Kind   string
	Config interface{} // contains secret-provider-specific config
}

func (kp KeyProvider) ValidationRules() []validate.ValidationRule {
	return []validate.ValidationRule{
		validate.Required("kind", kp.Kind),
	}
}

type nativeKeyProviderConfig struct {
	SecretProvider string
}

type AWSConfig struct {
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
}

func (c AWSConfig) ValidationRules() []validate.ValidationRule {
	return []validate.ValidationRule{
		validate.Required("endpoint", c.Endpoint),
		validate.Required("region", c.Region),
		validate.Required("accessKeyID", c.AccessKeyID),
		validate.Required("secretAccessKey", c.SecretAccessKey),
	}
}

type AWSKMSConfig struct {
	AWSConfig

	EncryptionAlgorithm string
	// aws tags?
}

type AWSSecretsManagerConfig struct {
	AWSConfig
}

type AWSSSMConfig struct {
	AWSConfig
	KeyID string // KMS key to use for decryption
}

func (c AWSSSMConfig) ValidationRules() []validate.ValidationRule {
	return []validate.ValidationRule{
		validate.Required("keyID", c.KeyID),
	}
}

type GenericConfig struct {
	Base64           bool
	Base64URLEncoded bool
	Base64Raw        bool
}

type FileConfig struct {
	GenericConfig
	Path string
}

func (c FileConfig) ValidationRules() []validate.ValidationRule {
	return []validate.ValidationRule{
		validate.Required("path", c.Path),
	}
}

type KubernetesConfig struct {
	Namespace string
}

type VaultConfig struct {
	TransitMount string // mounting point. defaults to /transit
	SecretMount  string // mounting point. defaults to /secret
	Token        string
	Namespace    string
	Address      string
}

func (c VaultConfig) ValidationRules() []validate.ValidationRule {
	return []validate.ValidationRule{
		validate.Required("token", c.Token),
		validate.Required("address", c.Address),
	}
}

func importKeyProviders(
	cfg []KeyProvider,
	storage map[string]secrets.SecretStorage,
	keys map[string]secrets.SymmetricKeyProvider,
) error {
	var err error

	// default to file-based native secret provider
	keys["native"] = secrets.NewNativeKeyProvider(storage["file"])

	for _, keyConfig := range cfg {
		switch keyConfig.Kind {
		case "native":
			cfg, ok := keyConfig.Config.(nativeKeyProviderConfig)
			if !ok {
				return fmt.Errorf("expected key config to be nativeKeyProviderConfig, but was %t", keyConfig.Config)
			}

			storageProvider, found := storage[cfg.SecretProvider]
			if !found {
				return fmt.Errorf("secret storage name %q not found", cfg.SecretProvider)
			}

			sp := secrets.NewNativeKeyProvider(storageProvider)
			keys[keyConfig.Kind] = sp
		case "awskms":
			cfg, ok := keyConfig.Config.(AWSKMSConfig)
			if !ok {
				return fmt.Errorf("expected key config to be AWSKMSConfig, but was %t", keyConfig.Config)
			}

			cfg.AccessKeyID, err = secrets.GetSecret(cfg.AccessKeyID, storage)
			if err != nil {
				return fmt.Errorf("getting secret for awskms accessKeyID: %w", err)
			}

			cfg.SecretAccessKey, err = secrets.GetSecret(cfg.SecretAccessKey, storage)
			if err != nil {
				return fmt.Errorf("getting secret for awskms secretAccessKey: %w", err)
			}

			kmsCfg := secrets.NewAWSKMSConfig()
			kmsCfg.AWSConfig.AccessKeyID = cfg.AccessKeyID
			kmsCfg.AWSConfig.Endpoint = cfg.Endpoint
			kmsCfg.AWSConfig.Region = cfg.Region
			kmsCfg.AWSConfig.SecretAccessKey = cfg.SecretAccessKey
			if len(cfg.EncryptionAlgorithm) > 0 {
				kmsCfg.EncryptionAlgorithm = cfg.EncryptionAlgorithm
			}

			sp, err := secrets.NewAWSKMSSecretProviderFromConfig(kmsCfg)
			if err != nil {
				return err
			}

			keys[keyConfig.Kind] = sp
		case "vault":
			cfg, ok := keyConfig.Config.(VaultConfig)
			if !ok {
				return fmt.Errorf("expected key config to be VaultConfig, but was %t", keyConfig.Config)
			}

			cfg.Token, err = secrets.GetSecret(cfg.Token, storage)
			if err != nil {
				return err
			}

			vcfg := secrets.NewVaultConfig()
			if len(cfg.TransitMount) > 0 {
				vcfg.TransitMount = cfg.TransitMount
			}
			if len(cfg.SecretMount) > 0 {
				vcfg.SecretMount = cfg.SecretMount
			}
			if len(cfg.Address) > 0 {
				vcfg.Address = cfg.Address
			}
			vcfg.Token = cfg.Token
			vcfg.Namespace = cfg.Namespace

			sp, err := secrets.NewVaultSecretProviderFromConfig(vcfg)
			if err != nil {
				return err
			}

			keys[keyConfig.Kind] = sp
		}
	}

	return nil
}

func (kp *KeyProvider) PrepareForDecode(data interface{}) error {
	if kp.Kind != "" {
		// this instance was already prepared from a previous call
		return nil
	}
	kind := getKindFromUnstructured(data)
	switch kind {
	case "vault":
		kp.Config = VaultConfig{}
	case "awskms":
		kp.Config = AWSKMSConfig{}
	case "native":
		kp.Config = nativeKeyProviderConfig{}
	default:
		// unknown kind error is handled by import importKeyProviders
	}

	return nil
}

type SecretProvider struct {
	Kind   string      `config:"kind"`
	Name   string      `config:"name"`
	Config interface{} // contains secret-provider-specific config
}

var baseSecretStorageKinds = []string{
	"env",
	"file",
	"plaintext",
	"kubernetes",
}

func isABaseSecretStorageKind(s string) bool {
	for _, item := range baseSecretStorageKinds {
		if item == s {
			return true
		}
	}

	return false
}

func importSecrets(cfg []SecretProvider, storage map[string]secrets.SecretStorage) error {
	loadSecretConfig := func(secret SecretProvider) (err error) {
		name := secret.Name
		if len(name) == 0 {
			name = secret.Kind
		}

		if _, found := storage[name]; found {
			return fmt.Errorf("duplicate secret configuration for %q, please provide a unique name for this secret configuration", name)
		}

		switch secret.Kind {
		case "vault":
			cfg, ok := secret.Config.(VaultConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be VaultConfig, but was %t", secret.Config)
			}

			cfg.Token, err = secrets.GetSecret(cfg.Token, storage)
			if err != nil {
				return err
			}

			vcfg := secrets.NewVaultConfig()
			if len(cfg.TransitMount) > 0 {
				vcfg.TransitMount = cfg.TransitMount
			}
			if len(cfg.SecretMount) > 0 {
				vcfg.SecretMount = cfg.SecretMount
			}
			if len(cfg.Address) > 0 {
				vcfg.Address = cfg.Address
			}
			vcfg.Token = cfg.Token
			vcfg.Namespace = cfg.Namespace

			vault, err := secrets.NewVaultSecretProviderFromConfig(vcfg)
			if err != nil {
				return fmt.Errorf("creating vault provider: %w", err)
			}

			storage[name] = vault
		case "awsssm":
			cfg, ok := secret.Config.(AWSSSMConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be AWSSSMConfig, but was %t", secret.Config)
			}

			cfg.AccessKeyID, err = secrets.GetSecret(cfg.AccessKeyID, storage)
			if err != nil {
				return err
			}

			cfg.SecretAccessKey, err = secrets.GetSecret(cfg.SecretAccessKey, storage)
			if err != nil {
				return err
			}

			ssmcfg := secrets.AWSSSMConfig{
				AWSConfig: secrets.AWSConfig{
					Endpoint:        cfg.Endpoint,
					Region:          cfg.Region,
					AccessKeyID:     cfg.AccessKeyID,
					SecretAccessKey: cfg.SecretAccessKey,
				},
				KeyID: cfg.KeyID,
			}

			ssm, err := secrets.NewAWSSSMSecretProviderFromConfig(ssmcfg)
			if err != nil {
				return fmt.Errorf("creating aws ssm: %w", err)
			}

			storage[name] = ssm
		case "awssecretsmanager":
			cfg, ok := secret.Config.(AWSSecretsManagerConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be AWSSecretsManagerConfig, but was %t", secret.Config)
			}

			cfg.AccessKeyID, err = secrets.GetSecret(cfg.AccessKeyID, storage)
			if err != nil {
				return err
			}

			cfg.SecretAccessKey, err = secrets.GetSecret(cfg.SecretAccessKey, storage)
			if err != nil {
				return err
			}

			smCfg := secrets.AWSSecretsManagerConfig{
				AWSConfig: secrets.AWSConfig{
					Endpoint:        cfg.Endpoint,
					Region:          cfg.Region,
					AccessKeyID:     cfg.AccessKeyID,
					SecretAccessKey: cfg.SecretAccessKey,
				},
			}

			sm, err := secrets.NewAWSSecretsManagerFromConfig(smCfg)
			if err != nil {
				return fmt.Errorf("creating aws sm: %w", err)
			}

			storage[name] = sm
		case "kubernetes":
			cfg, ok := secret.Config.(KubernetesConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be KubernetesConfig, but was %t", secret.Config)
			}

			kcfg := secrets.NewKubernetesConfig()
			if len(cfg.Namespace) > 0 {
				kcfg.Namespace = cfg.Namespace
			}

			k8s, err := secrets.NewKubernetesSecretProviderFromConfig(kcfg)
			if err != nil {
				return fmt.Errorf("creating k8s secret provider: %w", err)
			}

			storage[name] = k8s
		case "env":
			cfg, ok := secret.Config.(GenericConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be GenericConfig, but was %t", secret.Config)
			}

			gcfg := secrets.GenericConfig{
				Base64:           cfg.Base64,
				Base64URLEncoded: cfg.Base64URLEncoded,
				Base64Raw:        cfg.Base64Raw,
			}

			f := secrets.NewEnvSecretProviderFromConfig(gcfg)
			storage[name] = f
		case "file":
			cfg, ok := secret.Config.(FileConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be FileConfig, but was %t", secret.Config)
			}

			fcfg := secrets.FileConfig{
				GenericConfig: secrets.GenericConfig{
					Base64:           cfg.Base64,
					Base64URLEncoded: cfg.Base64URLEncoded,
					Base64Raw:        cfg.Base64Raw,
				},
				Path: cfg.Path,
			}

			f := secrets.NewFileSecretProviderFromConfig(fcfg)
			storage[name] = f
		case "plaintext", "":
			cfg, ok := secret.Config.(GenericConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be GenericConfig, but was %t", secret.Config)
			}

			gcfg := secrets.GenericConfig{
				Base64:           cfg.Base64,
				Base64URLEncoded: cfg.Base64URLEncoded,
				Base64Raw:        cfg.Base64Raw,
			}

			f := secrets.NewPlainSecretProviderFromConfig(gcfg)
			storage[name] = f
		default:
			return fmt.Errorf("unknown secret provider type %q", secret.Kind)
		}

		return nil
	}

	// check all base types first
	for _, secret := range cfg {
		if !isABaseSecretStorageKind(secret.Kind) {
			continue
		}

		if err := loadSecretConfig(secret); err != nil {
			return err
		}
	}

	if err := loadDefaultSecretConfig(storage); err != nil {
		return err
	}

	// now load non-base types which might depend on them.
	for _, secret := range cfg {
		if isABaseSecretStorageKind(secret.Kind) {
			continue
		}

		if err := loadSecretConfig(secret); err != nil {
			return err
		}
	}

	return nil
}

// loadDefaultSecretConfig loads configuration for types that should be available,
// assuming the user didn't override the configuration for them.
func loadDefaultSecretConfig(storage map[string]secrets.SecretStorage) error {
	// set up the default supported types
	if _, found := storage["env"]; !found {
		f := secrets.NewEnvSecretProviderFromConfig(secrets.GenericConfig{})
		storage["env"] = f
	}

	if _, found := storage["file"]; !found {
		f := secrets.NewFileSecretProviderFromConfig(secrets.FileConfig{})
		storage["file"] = f
	}

	if _, found := storage["plaintext"]; !found {
		f := secrets.NewPlainSecretProviderFromConfig(secrets.GenericConfig{})
		storage["plaintext"] = f
	}

	if _, found := storage["kubernetes"]; !found {
		// only setup k8s automatically if KUBERNETES_SERVICE_HOST is defined; ie, we are in the clustes.
		if _, ok := os.LookupEnv("KUBERNETES_SERVICE_HOST"); ok {
			k8s, err := secrets.NewKubernetesSecretProviderFromConfig(secrets.NewKubernetesConfig())
			if err != nil {
				return fmt.Errorf("creating k8s secret provider: %w", err)
			}

			storage["kubernetes"] = k8s
		}
	}

	return nil
}

// PrepareForDecode prepares the SecretProvider for mapstructure.Decode by
// setting a concrete type for the config based on the kind. Failures to decode
// will be handled by mapstructure, or by importSecrets.
func (sp *SecretProvider) PrepareForDecode(data interface{}) error {
	if sp.Kind != "" {
		// this instance was already prepared from a previous call
		return nil
	}
	kind := getKindFromUnstructured(data)
	switch kind {
	case "vault":
		sp.Config = VaultConfig{}
	case "awsssm":
		sp.Config = AWSSSMConfig{}
	case "awssecretsmanager":
		sp.Config = AWSSecretsManagerConfig{}
	case "kubernetes":
		sp.Config = KubernetesConfig{}
	case "env":
		sp.Config = GenericConfig{}
	case "file":
		sp.Config = FileConfig{}
	case "plaintext", "":
		sp.Kind = "plaintext"
		sp.Config = GenericConfig{}
	default:
		// unknown kind error is handled by importSecrets
	}

	return nil
}

func getKindFromUnstructured(data interface{}) string {
	switch raw := data.(type) {
	case map[string]interface{}:
		if v, ok := raw["kind"].(string); ok {
			return v
		}
	case map[interface{}]interface{}:
		if v, ok := raw["kind"].(string); ok {
			return v
		}
	case *SecretProvider:
		return raw.Kind
	}
	return ""
}

func (s Server) loadConfig(config BootstrapConfig) error {
	if err := validate.Validate(config); err != nil {
		return err
	}

	org := s.db.DefaultOrg

	tx, err := s.db.Begin(context.Background(), nil)
	if err != nil {
		return err
	}
	defer logError(tx.Rollback, "failed to rollback loadConfig transaction")
	tx = tx.WithOrgID(org.ID)

	if config.DefaultOrganizationDomain != org.Domain {
		org.Domain = config.DefaultOrganizationDomain
		if err := data.UpdateOrganization(tx, org); err != nil {
			return fmt.Errorf("update default org domain: %w", err)
		}
	}

	for _, u := range config.Users {
		if err := s.loadUser(tx, u); err != nil {
			return fmt.Errorf("load user %v: %w", u.Name, err)
		}
	}

	return tx.Commit()
}

func loadGrant(tx data.WriteTxn, userID uid.ID, role string) error {
	if role == "" {
		return nil
	}
	_, err := data.GetGrant(tx, data.GetGrantOptions{
		BySubject:   uid.NewIdentityPolymorphicID(userID),
		ByResource:  access.ResourceInfraAPI,
		ByPrivilege: role,
	})
	if err == nil || !errors.Is(err, internal.ErrNotFound) {
		return err
	}

	grant := &models.Grant{
		Subject:   uid.NewIdentityPolymorphicID(userID),
		Resource:  access.ResourceInfraAPI,
		Privilege: role,
		CreatedBy: models.CreatedBySystem,
	}
	return data.CreateGrant(tx, grant)
}

func (s Server) loadUser(db data.WriteTxn, input User) error {
	identity, err := data.GetIdentity(db, data.GetIdentityOptions{ByName: input.Name})
	if err != nil {
		if !errors.Is(err, internal.ErrNotFound) {
			return err
		}

		if input.Name != models.InternalInfraConnectorIdentityName {
			_, err := mail.ParseAddress(input.Name)
			if err != nil {
				logging.Warnf("user name %q in server configuration is not a valid email, please update this name to a valid email", input.Name)
			}
		}

		identity = &models.Identity{
			Name:      input.Name,
			CreatedBy: models.CreatedBySystem,
		}

		if err := data.CreateIdentity(db, identity); err != nil {
			return err
		}

		_, err = data.CreateProviderUser(db, data.InfraProvider(db), identity)
		if err != nil {
			return err
		}
	}

	if err := s.loadCredential(db, identity, input.Password); err != nil {
		return err
	}

	if err := s.loadAccessKey(db, identity, input.AccessKey); err != nil {
		return err
	}

	if err := loadGrant(db, identity.ID, input.InfraRole); err != nil {
		return err
	}

	return nil
}

func (s Server) loadCredential(db data.WriteTxn, identity *models.Identity, password string) error {
	if password == "" {
		return nil
	}

	password, err := secrets.GetSecret(password, s.secrets)
	if err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	credential, err := data.GetCredentialByUserID(db, identity.ID)
	if err != nil {
		if !errors.Is(err, internal.ErrNotFound) {
			return err
		}

		credential := &models.Credential{
			IdentityID:   identity.ID,
			PasswordHash: hash,
		}

		if err := data.CreateCredential(db, credential); err != nil {
			return err
		}

		if _, err := data.CreateProviderUser(db, data.InfraProvider(db), identity); err != nil {
			return err
		}

		return nil
	}

	credential.PasswordHash = hash

	return data.UpdateCredential(db, credential)
}

func (s Server) loadAccessKey(db data.WriteTxn, identity *models.Identity, key string) error {
	if key == "" {
		return nil
	}

	key, err := secrets.GetSecret(key, s.secrets)
	if err != nil {
		return err
	}

	keyID, secret, ok := strings.Cut(key, ".")
	if !ok {
		return fmt.Errorf("invalid access key format")
	}

	accessKey, err := data.GetAccessKeyByKeyID(db, keyID)
	if err != nil {
		if !errors.Is(err, internal.ErrNotFound) {
			return err
		}

		provider := data.InfraProvider(db)

		accessKey := &models.AccessKey{
			IssuedFor:  identity.ID,
			ExpiresAt:  time.Now().AddDate(10, 0, 0),
			KeyID:      keyID,
			Secret:     secret,
			ProviderID: provider.ID,
			Scopes:     models.CommaSeparatedStrings{models.ScopeAllowCreateAccessKey}, // allows user to create access keys
		}

		if _, err := data.CreateAccessKey(db, accessKey); err != nil {
			return err
		}

		if _, err := data.CreateProviderUser(db, provider, identity); err != nil {
			return err
		}

		return nil
	}

	if accessKey.IssuedFor != identity.ID {
		return fmt.Errorf("access key assigned to %q is already assigned to another user, a user's access key must have a unique ID", identity.Name)
	}

	accessKey.Secret = secret

	return data.UpdateAccessKey(db, accessKey)
}
