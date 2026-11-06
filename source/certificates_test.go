package main

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStationCertPoolUsesAvailableFirmwareBundleAfterMissingPath(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	certificate := server.Certificate()
	path := filepath.Join(t.TempDir(), "firmware-ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := stationCertPool(path+".missing", path)
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1"}); err != nil {
		t.Fatal("firmware CA was not loaded:", err)
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: stationCertPool(path + ".missing"), DNSName: "127.0.0.1"}); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}
