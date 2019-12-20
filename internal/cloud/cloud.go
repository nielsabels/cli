package cloud

import (
	"crypto/rand"
	"encoding/pem"

	"github.com/mikesmitty/edkey"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/crypto/ssh"
)

const (
	// DigitalOcean represents the DigitalOcean cloud provider
	DigitalOcean = "digitalocean"
	// Scaleway represents the Scaleway cloud provider
	Scaleway = "scaleway"
)

// SupportedProviders returns a list of supported cloud providers
func SupportedProviders() []string {
	return []string{Scaleway}
}

// Client allows interactions with cloud instances and images
type Client interface {
	NewInstance()
	DeleteInstance()
	StartInstance()
	StopInstance()
	AddImage(url string, hash string) error
	RemoveImage()
	AuthFields() []string
	Init(auth map[string]string) error
}

// NewClient creates a new cloud provider client
func NewClient(cloud string) (Client, error) {
	var client Client
	var err error
	switch cloud {
	case DigitalOcean:
		client, err = newDigitalOceanClient()
	case Scaleway:
		client, err = newScalewayClient()
	default:
		err = errors.Errorf("Cloud '%s' not supported", cloud)
	}
	if err != nil {
		return nil, err
	}
	return client, nil
}

func generateSSHkey() ([]byte, string, error) {
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", errors.Wrap(err, "Failed to generate SSH key")
	}
	publicKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return nil, "", errors.Wrap(err, "Failed to generate SSH key")
	}

	pemKey := &pem.Block{
		Type:  "OPENSSH PRIVATE KEY",
		Bytes: edkey.MarshalED25519PrivateKey(privKey),
	}
	privateKey := pem.EncodeToMemory(pemKey)
	authorizedKey := ssh.MarshalAuthorizedKey(publicKey)
	return privateKey, string(authorizedKey), nil
}
