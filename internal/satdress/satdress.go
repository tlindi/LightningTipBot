package satdress

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// from github.com/fiatjaf/makeinvoice

var TorProxyURL = "socks5://127.0.0.1:9050"
var Client = &http.Client{
	Timeout: 10 * time.Second,
}

type LNDParams struct {
	Cert     string
	Host     string
	Macaroon string
}

func (l LNDParams) getCert() string { return l.Cert }
func (l LNDParams) isTor() bool     { return strings.Index(l.Host, ".onion") != -1 }

type BackendParams interface {
	getCert() string
	isTor() bool
}

type Params struct {
	Backend         BackendParams
	Msatoshi        int64
	Description     string
	DescriptionHash []byte

	Label string // only used for c-lightning
}

func GetInvoice(params Params) (string, error) {

	defer func(prevTransport http.RoundTripper) {
		Client.Transport = prevTransport
	}(Client.Transport)

	specialTransport := &http.Transport{}

	// use a cert or skip TLS verification?
	if params.Backend.getCert() != "" {
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM([]byte(params.Backend.getCert()))
		specialTransport.TLSClientConfig = &tls.Config{RootCAs: caCertPool}
	} else {
		specialTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	// use a tor proxy?
	if params.Backend.isTor() {
		torURL, _ := url.Parse(TorProxyURL)
		specialTransport.Proxy = http.ProxyURL(torURL)
	}

	Client.Transport = specialTransport

	// description hash?
	var _, b64h string
	if params.DescriptionHash != nil {
		_ = hex.EncodeToString(params.DescriptionHash)
		b64h = base64.StdEncoding.EncodeToString(params.DescriptionHash)
	}

	switch backend := params.Backend.(type) {
	case LNDParams:
		body, _ := sjson.Set("{}", "value_msat", params.Msatoshi)

		if params.DescriptionHash == nil {
			body, _ = sjson.Set(body, "memo", params.Description)
		} else {
			body, _ = sjson.Set(body, "description_hash", b64h)
		}

		req, err := http.NewRequest("POST",
			backend.Host+"/v1/invoices",
			bytes.NewBufferString(body),
		)
		if err != nil {
			return "", err
		}

		// macaroon must be hex, so if it is on base64 we adjust that
		if b, err := base64.StdEncoding.DecodeString(backend.Macaroon); err == nil {
			backend.Macaroon = hex.EncodeToString(b)
		}

		req.Header.Set("Grpc-Metadata-macaroon", backend.Macaroon)
		resp, err := Client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			body, _ := ioutil.ReadAll(resp.Body)
			text := string(body)
			if len(text) > 300 {
				text = text[:300]
			}
			return "", fmt.Errorf("call to lnd failed (%d): %s", resp.StatusCode, text)
		}

		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		return gjson.ParseBytes(b).Get("payment_request").String(), nil
	}
	return "", errors.New("missing backend params")
}
