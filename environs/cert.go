package environs

import (
	"fmt"
	"io/ioutil"
	"launchpad.net/juju-core/cert"
	"launchpad.net/juju-core/environs/config"
	"os"
	"path/filepath"
	"time"
)

type CreatedCert bool

const (
	CertCreated CreatedCert = true
	CertExists  CreatedCert = false
)

func writeCertAndKeyToHome(name string, cert, key []byte) error {
	path := filepath.Join(os.Getenv("HOME"), ".juju", name)
	if err := ioutil.WriteFile(path+"-cert.pem", cert, 0644); err != nil {
		return err
	}
	if err := ioutil.WriteFile(path+"-private-key.pem", key, 0600); err != nil {
		return err
	}
	return nil
}

func generateCertificate(environ Environ) error {
	cfg := environ.Config()
	caCert, caKey, err := cert.NewCA(environ.Name(), time.Now().UTC().AddDate(10, 0, 0))
	if err != nil {
		return err
	}
	m := cfg.AllAttrs()
	m["ca-cert"] = string(caCert)
	m["ca-private-key"] = string(caKey)
	cfg, err = config.New(m)
	if err != nil {
		return fmt.Errorf("cannot create environment configuration with new CA: %v", err)
	}
	if err := environ.SetConfig(cfg); err != nil {
		return fmt.Errorf("cannot set environment configuration with CA: %v", err)
	}
	if err := writeCertAndKeyToHome(environ.Name(), caCert, caKey); err != nil {
		return fmt.Errorf("cannot write CA certificate and key: %v", err)
	}
	return nil
}

// EnsureCertificate makes sure that there is a certificate and private key
// for the specified environment.  If one does not exist, then a certificate
// is generated.
func EnsureCertificate(environ Environ) (CreatedCert, error) {
	cfg := environ.Config()
	_, hasCACert := cfg.CACert()
	_, hasCAKey := cfg.CAPrivateKey()

	if hasCACert && hasCAKey {
		// All is good in the world.
		return CertExists, nil
	}
	// It is not possible to create an environment that has a private key, but no certificate.
	if hasCACert && !hasCAKey {
		return CertExists, fmt.Errorf("environment configuration with a certificate but no CA private key")
	}

	return CertCreated, generateCertificate(environ)
}
