// Copyright 2015 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package remote

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"

	log "github.com/coreos/flannel/Godeps/_workspace/src/github.com/golang/glog"
	"github.com/coreos/flannel/Godeps/_workspace/src/github.com/gorilla/mux"
	"github.com/coreos/flannel/Godeps/_workspace/src/golang.org/x/net/context"

	"github.com/coreos/flannel/subnet"
)

type handler func(context.Context, subnet.Manager, http.ResponseWriter, *http.Request)

func jsonResponse(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error("Error JSON encoding response: %v", err)
	}
}

// GET /{network}/config
func handleGetNetworkConfig(ctx context.Context, sm subnet.Manager, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	network := mux.Vars(r)["network"]
	if network == "_" {
		network = ""
	}

	c, err := sm.GetNetworkConfig(ctx, network)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, err)
		return
	}

	jsonResponse(w, http.StatusOK, c)
}

// POST /{network}/leases
func handleAcquireLease(ctx context.Context, sm subnet.Manager, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	network := mux.Vars(r)["network"]
	if network == "_" {
		network = ""
	}

	attrs := subnet.LeaseAttrs{}
	if err := json.NewDecoder(r.Body).Decode(&attrs); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "JSON decoding error: ", err)
		return
	}

	lease, err := sm.AcquireLease(ctx, network, &attrs)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, err)
		return
	}

	jsonResponse(w, http.StatusOK, lease)
}

// PUT /{network}/{lease.network}
func handleRenewLease(ctx context.Context, sm subnet.Manager, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	network := mux.Vars(r)["network"]
	if network == "_" {
		network = ""
	}

	lease := subnet.Lease{}
	if err := json.NewDecoder(r.Body).Decode(&lease); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "JSON decoding error: ", err)
		return
	}

	if err := sm.RenewLease(ctx, network, &lease); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, err)
		return
	}

	jsonResponse(w, http.StatusOK, lease)
}

func getCursor(u *url.URL) (interface{}, error) {
	vals, ok := u.Query()["next"]
	if !ok {
		return nil, nil
	}
	index, err := strconv.ParseUint(vals[0], 10, 64)
	return index, err
}

// GET /{network}/leases?next=cursor
func handleWatchLeases(ctx context.Context, sm subnet.Manager, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	network := mux.Vars(r)["network"]
	if network == "_" {
		network = ""
	}

	cursor, err := getCursor(r.URL)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "invalid 'next' value: ", err)
		return
	}

	wr, err := sm.WatchLeases(ctx, network, cursor)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, err)
		return
	}

	jsonResponse(w, http.StatusOK, wr)
}

func bindHandler(h handler, ctx context.Context, sm subnet.Manager) http.HandlerFunc {
	return func(resp http.ResponseWriter, req *http.Request) {
		h(ctx, sm, resp, req)
	}
}

func RunServer(ctx context.Context, sm subnet.Manager, listenAddr string) {
	// {network} is always required a the API level but to
	// keep backward compat, special "_" network is allowed
	// that means "no network"

	r := mux.NewRouter()
	r.HandleFunc("/{network}/config", bindHandler(handleGetNetworkConfig, ctx, sm)).Methods("GET")
	r.HandleFunc("/{network}/leases", bindHandler(handleAcquireLease, ctx, sm)).Methods("POST")
	r.HandleFunc("/{network}/leases/{subnet}", bindHandler(handleRenewLease, ctx, sm)).Methods("PUT")
	r.HandleFunc("/{network}/leases", bindHandler(handleWatchLeases, ctx, sm)).Methods("GET")

	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Errorf("Error listening on %v: %v", listenAddr, err)
		return
	}

	c := make(chan error, 1)
	go func() {
		c <- http.Serve(l, httpLogger(r))
	}()

	select {
	case <-ctx.Done():
		l.Close()
		<-c

	case err := <-c:
		log.Errorf("Error serving on %v: %v", listenAddr, err)
	}
}
