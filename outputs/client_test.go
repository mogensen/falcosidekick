package outputs

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falcosecurity/falcosidekick/types"
)

var falcoTestInput = `{"output":"This is a test from falcosidekick","priority":"Debug","rule":"Test rule", "time":"2001-01-01T01:10:00Z","output_fields": {"proc.name":"falcosidekick", "proc.tty": 1234}}`

func TestNewClient(t *testing.T) {
	u, _ := url.Parse("http://localhost")

	config := &types.Configuration{}
	stats := &types.Statistics{}
	promStats := &types.PromStatistics{}

	testClientOutput := Client{OutputType: "test", EndpointURL: u, MutualTLSEnabled: false, Config: config, Stats: stats, PromStats: promStats}
	_, err := NewClient("test", "localhost/%*$¨^!/:;", false, true, config, stats, promStats, nil, nil)
	require.NotNil(t, err)

	nc, err := NewClient("test", "http://localhost", false, true, config, stats, promStats, nil, nil)
	require.Nil(t, err)
	require.Equal(t, &testClientOutput, nc)
}

func TestPost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("expected method : POST, got %s\n", r.Method)
		}
		switch r.URL.EscapedPath() {
		case "/200":
			w.WriteHeader(http.StatusOK)
		case "/400":
			w.WriteHeader(http.StatusBadRequest)
		case "/401":
			w.WriteHeader(http.StatusUnauthorized)
		case "/403":
			w.WriteHeader(http.StatusForbidden)
		case "/404":
			w.WriteHeader(http.StatusNotFound)
		case "/422":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "/429":
			w.WriteHeader(http.StatusTooManyRequests)
		case "/502":
			w.WriteHeader(http.StatusBadGateway)
		}
	}))

	for i, j := range map[string]error{
		"/200": nil, "/400": ErrHeaderMissing,
		"/401": ErrClientAuthenticationError,
		"/403": ErrForbidden,
		"/404": ErrNotFound,
		"/422": ErrUnprocessableEntityError,
		"/429": ErrTooManyRequest,
		"/502": errors.New("502 Bad Gateway"),
	} {
		nc, err := NewClient("", ts.URL+i, false, true, &types.Configuration{}, &types.Statistics{}, &types.PromStatistics{}, nil, nil)
		require.Nil(t, err)
		require.NotEmpty(t, nc)

		errPost := nc.Post("")
		require.Equal(t, errPost, j)
	}
}

func TestMutualTlsPost(t *testing.T) {
	config := &types.Configuration{}
	config.MutualTLSFilesPath = "/tmp/falcosidekicktests"
	// delete folder to avoid makedir failure
	os.RemoveAll(config.MutualTLSFilesPath)

	serverTLSConf, err := certsetup(config)
	if err != nil {
		require.Nil(t, err)
	}

	tlsURL := "127.0.0.1:5443"

	// set up the httptest.Server using our certificate signed by our CA
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("expected method : POST, got %s\n", r.Method)
		}
		if r.URL.EscapedPath() == "/200" {
			w.WriteHeader(http.StatusOK)
		}
	}))
	// This Listen config is required since server.URL generates a "Server already started" Panic error
	// Check https://golang.org/src/net/http/httptest/server.go#:~:text=s.URL
	l, _ := net.Listen("tcp", tlsURL)
	server.Listener = l
	server.TLS = serverTLSConf
	server.StartTLS()
	defer server.Close()

	nc, err := NewClient("", server.URL+"/200", true, true, config, &types.Statistics{}, &types.PromStatistics{}, nil, nil)
	require.Nil(t, err)
	require.NotEmpty(t, nc)

	errPost := nc.Post("")
	require.Nil(t, errPost)

}

func certsetup(config *types.Configuration) (serverTLSConf *tls.Config, err error) {
	err = os.Mkdir(config.MutualTLSFilesPath, 0755)
	if err != nil {
		return nil, err
	}

	// set up our CA certificate
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(2019),
		Subject: pkix.Name{
			Organization:  []string{"Sysdig"},
			Country:       []string{"US"},
			Province:      []string{""},
			Locality:      []string{"San Francisco"},
			StreetAddress: []string{"Falco st"},
			PostalCode:    []string{"94016"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	// create our private and public key
	caPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, err
	}

	// create the CA
	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, err
	}

	// pem encode
	caPEM := new(bytes.Buffer)
	pem.Encode(caPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caBytes,
	})

	// save ca to ca.crt file (it will be used by Client)
	err = ioutil.WriteFile(config.MutualTLSFilesPath+"/ca.crt", caPEM.Bytes(), 0600)
	if err != nil {
		return nil, err
	}

	// set up our server certificate
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(2019),
		Subject: pkix.Name{
			Organization:  []string{"Falco"},
			Country:       []string{"US"},
			Province:      []string{""},
			Locality:      []string{"San Francisco"},
			StreetAddress: []string{"Falcosidekick st"},
			PostalCode:    []string{"94016"},
		},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		SubjectKeyId: []byte{1, 2, 3, 4, 6},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	// create server private key
	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, err
	}

	// sign server certificate with CA key
	certBytes, err := x509.CreateCertificate(rand.Reader, cert, ca, &certPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, err
	}

	certPEM := new(bytes.Buffer)
	pem.Encode(certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})

	certPrivKeyPEM := new(bytes.Buffer)
	pem.Encode(certPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey),
	})
	serverCert, err := tls.X509KeyPair(certPEM.Bytes(), certPrivKeyPEM.Bytes())
	if err != nil {
		return nil, err
	}

	// create server TLS config
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caPEM.Bytes())
	serverTLSConf = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caCertPool,
		RootCAs:      caCertPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}

	// create client certificate
	clientCert := &x509.Certificate{
		SerialNumber: big.NewInt(2019),
		Subject: pkix.Name{
			Organization:  []string{"Falcosidekick"},
			Country:       []string{"US"},
			Province:      []string{""},
			Locality:      []string{"San Francisco"},
			StreetAddress: []string{"Falcosidekickclient st"},
			PostalCode:    []string{"94016"},
		},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		SubjectKeyId: []byte{1, 2, 3, 4, 6},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	// create client private key
	clientCertPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, err
	}

	// sign client certificate with CA key
	clientCertBytes, err := x509.CreateCertificate(rand.Reader, clientCert, ca, &clientCertPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, err
	}

	clientCertPEM := new(bytes.Buffer)
	pem.Encode(clientCertPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: clientCertBytes,
	})

	// save client cert and key to client.crt and client.key
	err = ioutil.WriteFile(config.MutualTLSFilesPath+"/client.crt", clientCertPEM.Bytes(), 0600)
	if err != nil {
		return nil, err
	}
	clientCertPrivKeyPEM := new(bytes.Buffer)
	pem.Encode(clientCertPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(clientCertPrivKey),
	})
	err = ioutil.WriteFile(config.MutualTLSFilesPath+"/client.key", clientCertPrivKeyPEM.Bytes(), 0600)
	if err != nil {
		return nil, err
	}
	return serverTLSConf, nil
}
