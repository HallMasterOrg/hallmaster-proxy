package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// serverNameFromHost strips an optional :port suffix so a hostname can be
// used as the Common Name of a leaf certificate.
func serverNameFromHost(hostname string) string {
	if !strings.Contains(hostname, ":") {
		return hostname
	}
	if host, _, err := net.SplitHostPort(hostname); err == nil {
		return host
	}
	return hostname
}

const (
	leafCertValidity    = 7 * 24 * time.Hour
	leafCertRenewBefore = 24 * time.Hour
)

// isExpiringSoon reports whether a cached leaf cert is within the renewal
// window. A nil cert (or one without a populated Leaf) is treated as
// expiring so it is always regenerated.
func isExpiringSoon(c *tls.Certificate) bool {
	if c == nil || c.Leaf == nil {
		return true
	}
	return time.Until(c.Leaf.NotAfter) < leafCertRenewBefore
}

type MITMProxyCerts struct {
	caCert *x509.Certificate
	caKey  *rsa.PrivateKey

	cache sync.Map // hostname -> *tls.Certificate
	group singleflight.Group
}

func New(caCertPath string, caKeyPath string) (*MITMProxyCerts, error) {
	caCert, err := getPublicKey(caCertPath)
	if err != nil {
		return nil, err
	}

	caKey, err := getPrivateKey(caKeyPath)
	if err != nil {
		return nil, err
	}

	return &MITMProxyCerts{
		caCert: caCert,
		caKey:  caKey,
	}, nil
}

func (p *MITMProxyCerts) GetOrCreateCert(hostname string) (*tls.Certificate, error) {
	if c, ok := p.cache.Load(hostname); ok {
		cert := c.(*tls.Certificate)
		if !isExpiringSoon(cert) {
			return cert, nil
		}
	}

	v, err, _ := p.group.Do(hostname, func() (any, error) {
		if c, ok := p.cache.Load(hostname); ok {
			cert := c.(*tls.Certificate)
			if !isExpiringSoon(cert) {
				return cert, nil
			}
		}
		c, err := p.createCertificate(hostname)
		if err != nil {
			return nil, err
		}
		p.cache.Store(hostname, c)
		return c, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*tls.Certificate), nil
}

func (c *MITMProxyCerts) createCertificate(hostname string) (*tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	servername := serverNameFromHost(hostname)

	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	subjectKeyId := sha1.Sum(pubBytes)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate cert serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: servername},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(leafCertValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              []string{servername},
		SubjectKeyId:          subjectKeyId[:],
		AuthorityKeyId:        c.caCert.SubjectKeyId,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, &priv.PublicKey, c.caKey)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.caCert.Raw})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	fullChain := append(certPEM, caPEM...)

	tlsCert, err := tls.X509KeyPair(fullChain, keyPEM)
	if err != nil {
		return nil, err
	}

	// tls.X509KeyPair leaves Leaf nil; populate it so the cache's
	// expiration check can read NotAfter without re-parsing.
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	tlsCert.Leaf = leaf

	return &tlsCert, nil
}

func getPublicKey(path string) (*x509.Certificate, error) {
	caCertPEM, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(caCertPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("invalid CA cert PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func getPrivateKey(path string) (*rsa.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// Refuse to load the CA private key if it is readable by anyone
	// besides the owner. The Hallmaster Root CA is the trust anchor for
	// every leaf cert this proxy issues; a world- or group-readable key
	// undermines that.
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("CA private key %s has permissive mode %#o; expected 0600 or stricter", path, info.Mode().Perm())
	}

	caKeyPEM, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(caKeyPEM)
	if block == nil || !strings.Contains(block.Type, "PRIVATE KEY") {
		return nil, fmt.Errorf("invalid CA key PEM")
	}
	caKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		keyIfc, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse CA key: %v / %v", err, err2)
		}
		var ok bool
		caKey, ok = keyIfc.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("CA key not RSA")
		}
	}

	return caKey, nil
}
