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
	"hallmasterorg/hallmaster-proxy/internals/utils"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"
)

type MITMProxyCerts struct {
	caCert *x509.Certificate
	caKey  *rsa.PrivateKey

	certCache map[string]*tls.Certificate
	mu        sync.Mutex
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
		caCert:           caCert,
		caKey:            caKey,
		certCache:        make(map[string]*tls.Certificate),
	}, nil
}

func (p *MITMProxyCerts) GetOrCreateCert(hostname string) (*tls.Certificate, error) {
	p.mu.Lock()
	if c, ok := p.certCache[hostname]; ok {
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()

	tlsCert, err := p.createCertificate(hostname)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.certCache[hostname] = tlsCert
	p.mu.Unlock()
	return tlsCert, nil
}

func (c *MITMProxyCerts) createCertificate(hostname string) (*tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	servername := utils.GetServerName(hostname)

    pubBytes, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
    subjectKeyId := sha1.Sum(pubBytes)

    serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
    tmpl := &x509.Certificate{
        SerialNumber: serial,
        Subject:      pkix.Name{CommonName: servername},
        NotBefore:    time.Now().Add(-time.Hour),
        NotAfter:     time.Now().Add(365 * 24 * time.Hour),
        KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
        ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
        BasicConstraintsValid: true,
        IsCA:                  false,
        DNSNames:             []string{servername},
        SubjectKeyId:   subjectKeyId[:],
        AuthorityKeyId: c.caCert.SubjectKeyId,
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
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}

	return caCert, nil
}

func getPrivateKey(path string) (*rsa.PrivateKey, error) {
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
