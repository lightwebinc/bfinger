package main

import (
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/bsv-blockchain/go-sdk/wallet/serializer"
	"github.com/bsv-blockchain/go-sdk/wallet/substrates"
)

// The SDK's HTTP wire client POSTs each call's parameters to base/<name>
// with the originator in the Origin header; the server rebuilds the request
// frame the processor expects and returns its result frame verbatim.
var callByName = map[string]substrates.Call{
	"createAction":                 substrates.CallCreateAction,
	"signAction":                   substrates.CallSignAction,
	"abortAction":                  substrates.CallAbortAction,
	"listActions":                  substrates.CallListActions,
	"internalizeAction":            substrates.CallInternalizeAction,
	"listOutputs":                  substrates.CallListOutputs,
	"relinquishOutput":             substrates.CallRelinquishOutput,
	"getPublicKey":                 substrates.CallGetPublicKey,
	"revealCounterpartyKeyLinkage": substrates.CallRevealCounterpartyKeyLinkage,
	"revealSpecificKeyLinkage":     substrates.CallRevealSpecificKeyLinkage,
	"encrypt":                      substrates.CallEncrypt,
	"decrypt":                      substrates.CallDecrypt,
	"createHmac":                   substrates.CallCreateHMAC,
	"verifyHmac":                   substrates.CallVerifyHMAC,
	"createSignature":              substrates.CallCreateSignature,
	"verifySignature":              substrates.CallVerifySignature,
	"acquireCertificate":           substrates.CallAcquireCertificate,
	"listCertificates":             substrates.CallListCertificates,
	"proveCertificate":             substrates.CallProveCertificate,
	"relinquishCertificate":        substrates.CallRelinquishCertificate,
	"discoverByIdentityKey":        substrates.CallDiscoverByIdentityKey,
	"discoverByAttributes":         substrates.CallDiscoverByAttributes,
	"isAuthenticated":              substrates.CallIsAuthenticated,
	"waitForAuthentication":        substrates.CallWaitForAuthentication,
	"getHeight":                    substrates.CallGetHeight,
	"getHeaderForHeight":           substrates.CallGetHeaderForHeight,
	"getNetwork":                   substrates.CallGetNetwork,
	"getVersion":                   substrates.CallGetVersion,
}

func serve(w wallet.Interface) http.Handler {
	proc := substrates.NewWalletWireProcessor(w)
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(rw, "wallet wire: POST a call", http.StatusMethodNotAllowed)
			return
		}
		call, ok := callByName[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.Error(rw, "wallet wire: unknown call", http.StatusNotFound)
			return
		}
		params, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
		if err != nil || len(params) > 1<<20 {
			http.Error(rw, "wallet wire: frame too large", http.StatusRequestEntityTooLarge)
			return
		}
		frame := serializer.WriteRequestFrame(serializer.RequestFrame{
			Call: byte(call), Originator: r.Header.Get("Origin"), Params: params,
		})
		out, err := proc.TransmitToWallet(r.Context(), frame)
		if err != nil {
			// The SDK's client reports only the status, so the reason is
			// logged here where an operator can read it.
			log.Printf("wire %s from %q: %v", strings.TrimPrefix(r.URL.Path, "/"), r.Header.Get("Origin"), err)
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		rw.Header().Set("Content-Type", "application/octet-stream")
		_, _ = rw.Write(out)
	})
}
