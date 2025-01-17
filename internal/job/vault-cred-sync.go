package job

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/intelops/go-common/logging"
	"github.com/intelops/vault-cred/config"
	"github.com/intelops/vault-cred/internal/api"
	"github.com/intelops/vault-cred/internal/client"
	"github.com/pkg/errors"
)

const (
	serviceCredSecretKeyPrefix   = "SERVICE-CRED"
	certSecretKeyPrefix          = "CERTS"
	genericSecretKeyPrefix       = "GENERIC"
	caDataKey                    = "ca.pem"
	certDataKey                  = "cert.crt"
	keyDataKey                   = "key.key"
	serviceCredentialUserNameKey = "userName"
	serviceCredentialPasswordKey = "password"
)

type CertificateData struct {
	EntityName      string `json:"entityName"`
	CertIndentifier string `json:"certIndetifier"`
	CACert          string `json:"caCert"`
	Key             string `json:"key"`
	Cert            string `json:"cert"`
}

type ServiceCredentail struct {
	EntityName      string            `json:"entityName"`
	CredIndentifier string            `json:"credIndetifier"`
	UserName        string            `json:"userName"`
	Password        string            `json:"password"`
	AdditionalData  map[string]string `json:"additionalData"`
}
type GenericCredential struct {
	CredentialType  string            `json:"credentialType"`
	EntityName      string            `json:"entityName"`
	CredIndentifier string            `json:"credIndetifier"`
	Credential      map[string]string `json:"credential"`
}
type VaultCredSync struct {
	log             logging.Logger
	conf            config.VaultEnv
	frequency       string
	lastUpdatedTime *time.Time
}

func NewVaultCredSync(log logging.Logger, frequency string) (*VaultCredSync, error) {
	conf, err := config.GetVaultEnv()
	if err != nil {
		return nil, err
	}
	return &VaultCredSync{
		log:       log,
		frequency: frequency,
		conf:      conf,
	}, nil
}

func (v *VaultCredSync) CronSpec() string {
	return v.frequency
}

func (v *VaultCredSync) Run() {
	v.log.Debug("started vault credential sync job")

	k8s, err := client.NewK8SClient(v.log)
	if err != nil {
		v.log.Errorf("failed to init k8s client, %s", err)
		return
	}

	ctx := context.Background()
	secretValues, err := k8s.GetSecret(ctx, v.conf.VaultCredSyncSecretName, v.conf.VaultSecretNameSpace)
	if err != nil {
		v.log.Debugf("failed to read sync secret, %s", err)
		return
	}
	v.log.Debugf("found %d secret values to sync", len(secretValues.Data))

	if v.lastUpdatedTime != nil {
		if v.lastUpdatedTime.Equal(secretValues.LastUpdatedTime) {
			v.log.Debugf("no change in secret")
			return
		}
	}

	vc, err := client.NewVaultClientForVaultToken(v.log, v.conf)
	if err != nil {
		v.log.Errorf("%s", err)
		return
	}

	for key, secretValue := range secretValues.Data {
		if strings.HasPrefix(key, serviceCredSecretKeyPrefix) {
			err = v.storeServiceCredential(ctx, vc, key, secretValue)
			if err != nil {
				v.log.Errorf("%s", err)
				continue
			}
		} else if strings.HasPrefix(key, certSecretKeyPrefix) {
			err = v.storeCertData(ctx, vc, key, secretValue)
			if err != nil {
				v.log.Errorf("%s", err)
				continue
			}
		} else if strings.HasPrefix(key, genericSecretKeyPrefix) {
			err = v.storeGenericCredential(ctx, vc, key, secretValue)
			if err != nil {
				v.log.Errorf("%s", err)
				continue
			}
		} else {
			v.log.Infof("credentail type %s not supported", key)
		}
	}

	updateTime := secretValues.LastUpdatedTime.Add(0)
	v.lastUpdatedTime = &updateTime
	v.log.Debug("vault credential sync job completed")
}

func (v *VaultCredSync) storeServiceCredential(ctx context.Context, vc *client.VaultClient, secretIdentifier, secretData string) error {
	var serviceCredData ServiceCredentail
	err := json.Unmarshal([]byte(secretData), &serviceCredData)
	if err != nil {
		return errors.WithMessagef(err, "failed to parse %s secret data", secretIdentifier)
	}

	if len(serviceCredData.UserName) == 0 || len(serviceCredData.Password) == 0 || len(serviceCredData.EntityName) == 0 {
		return errors.WithMessagef(err, "credential attributes are emty for %s secret data", secretIdentifier)
	}

	cred := map[string]string{serviceCredentialUserNameKey: serviceCredData.UserName,
		serviceCredentialPasswordKey: serviceCredData.Password}
	for key, val := range serviceCredData.AdditionalData {
		cred[key] = val
	}

	secretPath := api.PrepareCredentialSecretPath(strings.ToLower(serviceCredSecretKeyPrefix), serviceCredData.EntityName, serviceCredData.CredIndentifier)
	err = vc.PutCredential(ctx, api.CredentialMountPath(), secretPath, cred)
	if err != nil {
		return errors.WithMessagef(err, "failed to write %s secret data to vault", secretIdentifier)
	}
	v.log.Infof("stored sync service credential for %s/%s", serviceCredData.EntityName, serviceCredData.CredIndentifier)
	return nil
}

func (v *VaultCredSync) storeCertData(ctx context.Context, vc *client.VaultClient, secretIdentifier, secretData string) error {
	var certData CertificateData
	err := json.Unmarshal([]byte(secretData), &certData)
	if err != nil {
		return errors.WithMessagef(err, "failed to parse %s secret data", secretIdentifier)
	}

	if len(certData.CACert) == 0 || len(certData.Cert) == 0 || len(certData.Key) == 0 ||
		len(certData.EntityName) == 0 || len(certData.CertIndentifier) == 0 {
		return errors.WithMessagef(err, "credential attributes are emty for %s secret data", secretIdentifier)
	}

	cred := map[string]string{caDataKey: certData.CACert,
		certDataKey: certData.Cert,
		keyDataKey:  certData.Key}

	secretPath := api.PrepareCredentialSecretPath(strings.ToLower(certSecretKeyPrefix), certData.EntityName, certData.CertIndentifier)
	err = vc.PutCredential(ctx, api.CredentialMountPath(), secretPath, cred)
	if err != nil {
		return errors.WithMessagef(err, "failed to write %s secret data to vault", secretIdentifier)
	}
	v.log.Infof("stored sync cert for %s/%s", certData.EntityName, certData.CertIndentifier)
	return nil
}

func (v *VaultCredSync) storeGenericCredential(ctx context.Context, vc *client.VaultClient, secretIdentifier, secretData string) error {
	var genericCredData GenericCredential
	err := json.Unmarshal([]byte(secretData), &genericCredData)
	if err != nil {
		return errors.WithMessagef(err, "failed to parse %s secret data", secretIdentifier)
	}

	if len(genericCredData.EntityName) == 0 || len(genericCredData.CredIndentifier) == 0 || len(genericCredData.CredentialType) == 0 {
		return errors.WithMessagef(err, "credential attributes are emty for %s secret data", secretIdentifier)
	}

	cred := map[string]string{}
	for key, val := range genericCredData.Credential {
		cred[key] = val
	}

	secretPath := api.PrepareCredentialSecretPath(genericCredData.CredentialType, genericCredData.EntityName, genericCredData.CredIndentifier)
	err = vc.PutCredential(ctx, api.CredentialMountPath(), secretPath, cred)
	if err != nil {
		return errors.WithMessagef(err, "failed to write %s secret data to vault", secretIdentifier)
	}
	v.log.Infof("stored sync credential for %s/%s/%s", genericCredData.CredentialType, genericCredData.EntityName, genericCredData.CredIndentifier)
	return nil

}
