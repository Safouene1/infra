package registry

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
	"gorm.io/gorm"

	"github.com/infrahq/infra/internal"
	"github.com/infrahq/infra/internal/access"
	"github.com/infrahq/infra/internal/generate"
	"github.com/infrahq/infra/internal/registry/data"
	"github.com/infrahq/infra/internal/registry/models"
	"github.com/infrahq/infra/secrets"
	"github.com/infrahq/infra/uid"
)

const oneHundredYears = time.Hour * 876000

type ConfigProvider struct {
	Kind         string `yaml:"kind" validate:"required"`
	Domain       string `yaml:"domain" validate:"required"`
	ClientID     string `yaml:"clientID" validate:"required"`
	ClientSecret string `yaml:"clientSecret" validate:"required"`
}

var (
	dashAdminRemover = regexp.MustCompile(`(.*)\-admin(\.okta\.com)`)
	protocolRemover  = regexp.MustCompile(`http[s]?://`)
)

func (p *ConfigProvider) cleanupDomain() {
	p.Domain = strings.TrimSpace(p.Domain)
	p.Domain = dashAdminRemover.ReplaceAllString(p.Domain, "$1$2")
	p.Domain = protocolRemover.ReplaceAllString(p.Domain, "")
}

type ConfigDestination struct {
	Name       string                 `yaml:"name"`
	Labels     []string               `yaml:"labels"`
	Kind       models.DestinationKind `yaml:"kind" validate:"required"`
	Namespaces []string               `yaml:"namespaces"` // optional in the case of a cluster-role
}

type ConfigGrant struct {
	Name         string                 `yaml:"name" validate:"required"`
	Kind         models.DestinationKind `yaml:"kind" validate:"required,oneof=role cluster-role"`
	Destinations []ConfigDestination    `yaml:"destinations" validate:"required,dive"`
}

type ConfigGroupMapping struct {
	Name     string        `yaml:"name" validate:"required"`
	Provider string        `yaml:"provider" validate:"required"`
	Grants   []ConfigGrant `yaml:"grants" validate:"required,dive"`
}

type ConfigUserMapping struct {
	Email  string        `yaml:"email" validate:"required,email"`
	Grants []ConfigGrant `yaml:"grants" validate:"required,dive"`
}

type ConfigSecretProvider struct {
	Kind   string      `yaml:"kind" validate:"required"`
	Name   string      `yaml:"name"` // optional
	Config interface{} // contains secret-provider-specific config
}

type ConfigSecretKeyProvider struct {
	Kind   string      `yaml:"kind" validate:"required"`
	Config interface{} // contains secret-provider-specific config
}

type simpleConfigSecretProvider struct {
	Kind string `yaml:"kind"`
	Name string `yaml:"name"`
}

// ensure these implements yaml.Unmarshaller for the custom config field support
var (
	_ yaml.Unmarshaler = &ConfigSecretProvider{}
	_ yaml.Unmarshaler = &ConfigSecretKeyProvider{}
)

func (sp *ConfigSecretKeyProvider) UnmarshalYAML(unmarshal func(interface{}) error) error {
	tmp := &simpleConfigSecretProvider{}

	if err := unmarshal(&tmp); err != nil {
		return fmt.Errorf("unmarshalling secret provider: %w", err)
	}

	sp.Kind = tmp.Kind

	switch sp.Kind {
	case "vault":
		p := secrets.NewVaultConfig()
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	case "awskms":
		p := secrets.NewAWSKMSConfig()
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		if err := unmarshal(&p.AWSConfig); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	case "native":
		p := nativeSecretProviderConfig{}
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	default:
		return fmt.Errorf("unknown key provider type %q, expected one of %q", sp.Kind, secrets.SymmetricKeyProviderKinds)
	}

	return nil
}

func (sp *ConfigSecretProvider) UnmarshalYAML(unmarshal func(interface{}) error) error {
	tmp := &simpleConfigSecretProvider{}

	if err := unmarshal(&tmp); err != nil {
		return fmt.Errorf("unmarshalling secret provider: %w", err)
	}

	sp.Kind = tmp.Kind
	sp.Name = tmp.Name

	switch tmp.Kind {
	case "vault":
		p := secrets.NewVaultConfig()
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	case "awsssm":
		p := secrets.AWSSSMConfig{}
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		if err := unmarshal(&p.AWSConfig); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	case "awssecretsmanager":
		p := secrets.AWSSecretsManagerConfig{}
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		if err := unmarshal(&p.AWSConfig); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	case "kubernetes":
		p := secrets.NewKubernetesConfig()
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	case "env":
		p := secrets.GenericConfig{}
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	case "file":
		p := secrets.FileConfig{}
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		if err := unmarshal(&p.GenericConfig); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	case "plaintext", "":
		p := secrets.GenericConfig{}
		if err := unmarshal(&p); err != nil {
			return fmt.Errorf("unmarshal yaml: %w", err)
		}

		sp.Config = p
	default:
		return fmt.Errorf("unknown secret provider type %q, expected one of %q", tmp.Kind, secrets.SecretStorageProviderKinds)
	}

	return nil
}

type Config struct {
	Secrets   []ConfigSecretProvider    `yaml:"secrets" validate:"dive"`
	Keys      []ConfigSecretKeyProvider `yaml:"keys" validate:"dive"`
	Providers []ConfigProvider          `yaml:"providers" validate:"dive"`
	Groups    []ConfigGroupMapping      `yaml:"groups" validate:"dive"`
	Users     []ConfigUserMapping       `yaml:"users" validate:"dive"`
}

func importProviders(db *gorm.DB, providers []ConfigProvider) error {
	toKeep := make([]uid.ID, 0)

	for _, p := range providers {
		p.cleanupDomain()

		// domain has been modified, so need to re-validate
		if err := validate.Struct(p); err != nil {
			return fmt.Errorf("invalid domain: %w", err)
		}

		provider := &models.Provider{
			Kind:         models.ProviderKind(p.Kind),
			Domain:       p.Domain,
			ClientID:     p.ClientID,
			ClientSecret: models.EncryptedAtRest(p.ClientSecret),
		}

		final, err := data.CreateOrUpdateProvider(db, provider)
		if err != nil {
			return err
		}

		toKeep = append(toKeep, final.ID)
	}

	if err := data.DeleteProviders(db, func(db *gorm.DB) *gorm.DB {
		return db.Model((*models.Provider)(nil)).Not(toKeep)
	}); err != nil {
		if !errors.Is(err, internal.ErrNotFound) {
			return err
		}
	}

	return nil
}

// importConfig tries to import all valid fields in a config file and removes old config
func (r *Registry) importConfig() error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := importProviders(tx, r.options.Providers); err != nil {
			return fmt.Errorf("providers: %w", err)
		}

		// todo: import grants

		return nil
	})
}

func (r *Registry) importAPITokens() error {
	type key struct {
		Secret      string
		Permissions []string
	}

	keys := map[string]key{
		"root": {
			Secret: r.options.RootAPIToken,
			Permissions: []string{
				string(access.PermissionAllInfra),
			},
		},
		"engine": {
			Secret: r.options.EngineAPIToken,
			Permissions: []string{
				string(access.PermissionGrantRead),
				string(access.PermissionDestinationRead),
				string(access.PermissionDestinationCreate),
				string(access.PermissionDestinationUpdate),
			},
		},
	}

	for k, v := range keys {
		tokenSecret, err := r.GetSecret(v.Secret)
		if err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return err
		}

		// if empty, generate it
		if tokenSecret == "" {
			tokenSecret, err = generate.CryptoRandom(36)
			if err != nil {
				return err
			}

			if err := r.SetSecret(v.Secret, tokenSecret); err != nil {
				return err
			}
		}

		if len(tokenSecret) != models.TokenLength {
			return fmt.Errorf("secret for %q token must be %d characters in length, but is %d", k, models.TokenLength, len(tokenSecret))
		}

		key, sec := models.KeyAndSecret(tokenSecret)

		existing, err := data.GetAPIToken(r.db, data.ByName(k))
		if err != nil {
			if !errors.Is(err, internal.ErrNotFound) {
				return err
			}

			apiToken := &models.APIToken{
				Name:        k,
				Permissions: strings.Join(v.Permissions, " "),
				TTL:         oneHundredYears,
			}

			if err := data.CreateAPIToken(r.db, apiToken); err != nil {
				return err
			}

			tkn := &models.Token{Key: key, Secret: sec, APITokenID: apiToken.ID, SessionDuration: apiToken.TTL}

			if err := data.CreateToken(r.db, tkn); err != nil {
				return fmt.Errorf("create api token from config: %w", err)
			}
		} else {
			existing.Permissions = strings.Join(v.Permissions, " ")
			existing.TTL = oneHundredYears

			err := data.UpdateAPIToken(r.db, existing)
			if err != nil {
				return err
			}

			// create or update associated token
			existingToken, err := data.GetToken(r.db, data.ByKey(key))
			if err != nil {
				if !errors.Is(err, internal.ErrNotFound) {
					return err
				}

				tkn := &models.Token{Key: key, Secret: sec, APITokenID: existing.ID, SessionDuration: existing.TTL}

				if err := data.CreateToken(r.db, tkn); err != nil {
					return fmt.Errorf("create token from config: %w", err)
				}

			} else {
				existingToken.APITokenID = existing.ID
				existingToken.Secret = sec
				existingToken.SessionDuration = existing.TTL

				if err := data.UpdateToken(r.db, existingToken, data.ByID(existingToken.ID)); err != nil {
					return fmt.Errorf("update token from config: %w", err)
				}
			}

		}
	}

	return nil
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

type nativeSecretProviderConfig struct {
	SecretStorageName string `yaml:"secretStorage"`
}

func (r *Registry) importSecretKeys() error {
	var err error

	if r.keys == nil {
		r.keys = map[string]secrets.SymmetricKeyProvider{}
	}

	// default to file-based native secret provider
	r.keys["native"] = secrets.NewNativeSecretProvider(r.secrets["file"])

	for _, keyConfig := range r.options.Keys {
		switch keyConfig.Kind {
		case "native":
			cfg, ok := keyConfig.Config.(nativeSecretProviderConfig)
			if !ok {
				return fmt.Errorf("expected key config to be NativeSecretProviderConfig, but was %t", keyConfig.Config)
			}

			storageProvider, found := r.secrets[cfg.SecretStorageName]
			if !found {
				return fmt.Errorf("secret provider name %q not found", cfg.SecretStorageName)
			}

			sp := secrets.NewNativeSecretProvider(storageProvider)
			r.keys[keyConfig.Kind] = sp
		case "awskms":
			cfg, ok := keyConfig.Config.(secrets.AWSKMSConfig)
			if !ok {
				return fmt.Errorf("expected key config to be AWSKMSConfig, but was %t", keyConfig.Config)
			}

			cfg.AccessKeyID, err = r.GetSecret(cfg.AccessKeyID)
			if err != nil {
				return fmt.Errorf("getting secret for awskms accessKeyID: %w", err)
			}

			cfg.SecretAccessKey, err = r.GetSecret(cfg.SecretAccessKey)
			if err != nil {
				return fmt.Errorf("getting secret for awskms secretAccessKey: %w", err)
			}

			sp, err := secrets.NewAWSKMSSecretProviderFromConfig(cfg)
			if err != nil {
				return err
			}

			r.keys[keyConfig.Kind] = sp
		case "vault":
			cfg, ok := keyConfig.Config.(secrets.VaultConfig)
			if !ok {
				return fmt.Errorf("expected key config to be VaultConfig, but was %t", keyConfig.Config)
			}

			cfg.Token, err = r.GetSecret(cfg.Token)
			if err != nil {
				return err
			}

			sp, err := secrets.NewVaultSecretProviderFromConfig(cfg)
			if err != nil {
				return err
			}

			r.keys[keyConfig.Kind] = sp
		}
	}

	return nil
}

func (r *Registry) importSecrets() error {
	if r.secrets == nil {
		r.secrets = map[string]secrets.SecretStorage{}
	}

	loadSecretConfig := func(secret ConfigSecretProvider) (err error) {
		name := secret.Name
		if len(name) == 0 {
			name = secret.Kind
		}

		if _, found := r.secrets[name]; found {
			return fmt.Errorf("duplicate secret configuration for %q, please provide a unique name for this secret configuration", name)
		}

		switch secret.Kind {
		case "vault":
			cfg, ok := secret.Config.(secrets.VaultConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be VaultConfig, but was %t", secret.Config)
			}

			cfg.Token, err = r.GetSecret(cfg.Token)
			if err != nil {
				return err
			}

			vault, err := secrets.NewVaultSecretProviderFromConfig(cfg)
			if err != nil {
				return fmt.Errorf("creating vault provider: %w", err)
			}

			r.secrets[name] = vault
		case "awsssm":
			cfg, ok := secret.Config.(secrets.AWSSSMConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be AWSSSMConfig, but was %t", secret.Config)
			}

			cfg.AccessKeyID, err = r.GetSecret(cfg.AccessKeyID)
			if err != nil {
				return err
			}

			cfg.SecretAccessKey, err = r.GetSecret(cfg.SecretAccessKey)
			if err != nil {
				return err
			}

			ssm, err := secrets.NewAWSSSMSecretProviderFromConfig(cfg)
			if err != nil {
				return fmt.Errorf("creating aws ssm: %w", err)
			}

			r.secrets[name] = ssm
		case "awssecretsmanager":
			cfg, ok := secret.Config.(secrets.AWSSecretsManagerConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be AWSSecretsManagerConfig, but was %t", secret.Config)
			}

			cfg.AccessKeyID, err = r.GetSecret(cfg.AccessKeyID)
			if err != nil {
				return err
			}

			cfg.SecretAccessKey, err = r.GetSecret(cfg.SecretAccessKey)
			if err != nil {
				return err
			}

			sm, err := secrets.NewAWSSecretsManagerFromConfig(cfg)
			if err != nil {
				return fmt.Errorf("creating aws sm: %w", err)
			}

			r.secrets[name] = sm
		case "kubernetes":
			cfg, ok := secret.Config.(secrets.KubernetesConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be KubernetesConfig, but was %t", secret.Config)
			}

			k8s, err := secrets.NewKubernetesSecretProviderFromConfig(cfg)
			if err != nil {
				return fmt.Errorf("creating k8s secret provider: %w", err)
			}

			r.secrets[name] = k8s
		case "env":
			cfg, ok := secret.Config.(secrets.GenericConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be GenericConfig, but was %t", secret.Config)
			}

			f := secrets.NewEnvSecretProviderFromConfig(cfg)
			r.secrets[name] = f
		case "file":
			cfg, ok := secret.Config.(secrets.FileConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be FileConfig, but was %t", secret.Config)
			}

			f := secrets.NewFileSecretProviderFromConfig(cfg)
			r.secrets[name] = f
		case "plaintext", "":
			cfg, ok := secret.Config.(secrets.GenericConfig)
			if !ok {
				return fmt.Errorf("expected secret config to be GenericConfig, but was %t", secret.Config)
			}

			f := secrets.NewPlainSecretProviderFromConfig(cfg)
			r.secrets[name] = f
		default:
			return fmt.Errorf("unknown secret provider type %q", secret.Kind)
		}

		return nil
	}

	// check all base types first
	for _, secret := range r.options.Secrets {
		if !isABaseSecretStorageKind(secret.Kind) {
			continue
		}

		if err := loadSecretConfig(secret); err != nil {
			return err
		}
	}

	if err := r.loadDefaultSecretConfig(); err != nil {
		return err
	}

	// now load non-base types which might depend on them.
	for _, secret := range r.options.Secrets {
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
func (r *Registry) loadDefaultSecretConfig() error {
	// set up the default supported types
	if _, found := r.secrets["env"]; !found {
		f := secrets.NewEnvSecretProviderFromConfig(secrets.GenericConfig{})
		r.secrets["env"] = f
	}

	if _, found := r.secrets["file"]; !found {
		f := secrets.NewFileSecretProviderFromConfig(secrets.FileConfig{})
		r.secrets["file"] = f
	}

	if _, found := r.secrets["plaintext"]; !found {
		f := secrets.NewPlainSecretProviderFromConfig(secrets.GenericConfig{})
		r.secrets["plaintext"] = f
	}

	if _, found := r.secrets["kubernetes"]; !found {
		// only setup k8s automatically if KUBERNETES_SERVICE_HOST is defined; ie, we are in the cluster.
		if _, ok := os.LookupEnv("KUBERNETES_SERVICE_HOST"); ok {
			k8s, err := secrets.NewKubernetesSecretProviderFromConfig(secrets.NewKubernetesConfig())
			if err != nil {
				return fmt.Errorf("creating k8s secret provider: %w", err)
			}

			r.secrets["kubernetes"] = k8s
		}
	}

	return nil
}
