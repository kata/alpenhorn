// Copyright 2016 David Lazar. All rights reserved.
// Use of this source code is governed by the GNU AGPL
// license that can be found in the LICENSE file.

package alpenhorn

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ed25519"

	"vuvuzela.io/alpenhorn/cdn"
	"vuvuzela.io/alpenhorn/coordinator"
	"vuvuzela.io/alpenhorn/edhttp"
	"vuvuzela.io/alpenhorn/edtls"
	"vuvuzela.io/alpenhorn/internal/debug"
	"vuvuzela.io/alpenhorn/internal/mock"
	"vuvuzela.io/alpenhorn/pkg"
	"vuvuzela.io/crypto/rand"
)

type chanHandler struct {
	errPrefix string

	confirmedFriend       chan *Friend
	sentFriendRequest     chan *OutgoingFriendRequest
	receivedFriendRequest chan *IncomingFriendRequest
	sentCall              chan *OutgoingCall
	receivedCall          chan *IncomingCall
}

func newChanHandler(errPrefix string) *chanHandler {
	return &chanHandler{
		errPrefix:             errPrefix,
		confirmedFriend:       make(chan *Friend, 1),
		sentFriendRequest:     make(chan *OutgoingFriendRequest, 1),
		receivedFriendRequest: make(chan *IncomingFriendRequest, 1),
		sentCall:              make(chan *OutgoingCall, 1),
		receivedCall:          make(chan *IncomingCall, 1),
	}
}

func (h *chanHandler) Error(err error) {
	log.Errorf(h.errPrefix+": client error: %s", err)
}
func (h *chanHandler) ConfirmedFriend(f *Friend) {
	h.confirmedFriend <- f
}
func (h *chanHandler) SentFriendRequest(r *OutgoingFriendRequest) {
	h.sentFriendRequest <- r
}
func (h *chanHandler) ReceivedFriendRequest(r *IncomingFriendRequest) {
	h.receivedFriendRequest <- r
}
func (h *chanHandler) SentCall(call *OutgoingCall) {
	h.sentCall <- call
}
func (h *chanHandler) ReceivedCall(call *IncomingCall) {
	h.receivedCall <- call
}
func (h *chanHandler) UnexpectedSigningKey(in *IncomingFriendRequest, out *OutgoingFriendRequest) {
	log.Fatalf("unexpected signing key for %s", in.Username)
}

func (u *universe) newUser(username string) *Client {
	pkgKeys := make([]ed25519.PublicKey, len(u.PKGs))
	pkgAddrs := make([]string, len(u.PKGs))
	for i, pkgServer := range u.PKGs {
		pkgKeys[i] = pkgServer.Key
		pkgAddrs[i] = pkgServer.Address
	}

	h := newChanHandler(username)

	userPub, userPriv, _ := ed25519.GenerateKey(rand.Reader)
	client := &Client{
		Username:           username,
		LongTermPublicKey:  userPub,
		LongTermPrivateKey: userPriv,
		PKGLoginKey:        userPriv,

		CoordinatorAddress: u.CoordinatorAddress,
		CoordinatorKey:     u.CoordinatorKey,

		Handler: h,
	}
	err := client.Bootstrap(
		u.addFriendServer.CurrentConfig(),
		u.dialingServer.CurrentConfig(),
	)
	if err != nil {
		log.Fatalf("client.Bootstrap: %s", err)
	}

	for _, pkgServer := range u.PKGs {
		err := client.Register(client.Username, pkgServer.Address, pkgServer.Key)
		if err != nil {
			log.Fatalf("client.Register: %s", err)
		}
	}

	return client
}

func TestAliceFriendsThenCallsBob(t *testing.T) {
	u := createAlpenhornUniverse()
	defer u.Destroy()

	alice := u.newUser("alice@example.org")
	bob := u.newUser("bob@example.org")
	bob.ClientPersistPath = filepath.Join(u.Dir, "bob-client")
	bob.KeywheelPersistPath = filepath.Join(u.Dir, "bob-keywheel")

	if err := alice.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := bob.Connect(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(2 * time.Second)

	_, err := alice.SendFriendRequest(bob.Username, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-alice.Handler.(*chanHandler).sentFriendRequest
	log.Printf("Alice: sent friend request")

	friendRequest := <-bob.Handler.(*chanHandler).receivedFriendRequest
	currentConfig := u.addFriendServer.CurrentConfig()
	if !reflect.DeepEqual(currentConfig.PKGServers, friendRequest.Verifiers) {
		t.Fatalf("unexpected verifiers list in friend request:\ngot:  %s\nwant: %s",
			debug.Pretty(friendRequest.Verifiers), debug.Pretty(currentConfig.PKGServers))
	}
	_, err = friendRequest.Approve()
	if err != nil {
		t.Fatal(err)
	}
	<-bob.Handler.(*chanHandler).sentFriendRequest
	log.Printf("Bob: approved friend request")

	aliceConfirmedFriend := <-alice.Handler.(*chanHandler).confirmedFriend
	if aliceConfirmedFriend.Username != bob.Username {
		t.Fatalf("made friends with unexpected username: %s", aliceConfirmedFriend.Username)
	}
	log.Printf("Alice: confirmed friend")

	bobConfirmedFriend := <-bob.Handler.(*chanHandler).confirmedFriend
	if bobConfirmedFriend.Username != alice.Username {
		t.Fatalf("made friends with unexpected username: %s", bobConfirmedFriend.Username)
	}
	log.Printf("Bob: confirmed friend")

	friend := alice.GetFriend(bob.Username)
	if friend == nil {
		t.Fatal("friend not found")
	}

	friend.Call(0)
	outCall := <-alice.Handler.(*chanHandler).sentCall
	log.Printf("Alice: called Bob")

	inCall := <-bob.Handler.(*chanHandler).receivedCall
	if inCall.Username != alice.Username {
		t.Fatalf("received call from unexpected username: %s", inCall.Username)
	}
	log.Printf("Bob: received call from Alice")

	if !bytes.Equal(outCall.SessionKey()[:], inCall.SessionKey[:]) {
		t.Fatal("Alice and Bob agreed on different keys!")
	}

	// Test persistence.
	if err := bob.Close(); err != nil {
		t.Fatal(err)
	}
	bob2, err := LoadClient(bob.ClientPersistPath)
	if err != nil {
		t.Fatal(err)
	}
	bob2.KeywheelPersistPath = bob.KeywheelPersistPath
	bob2.Handler = newChanHandler("bob2")
	if err := bob2.Connect(); err != nil {
		t.Fatal(err)
	}

	friend = bob2.GetFriend(alice.Username)
	friend.Call(0)
	outCall = <-bob2.Handler.(*chanHandler).sentCall
	if outCall.Username != alice.Username {
		t.Fatalf("bad username in call: got %q, want %q", outCall.Username, alice.Username)
	}

	inCall = <-alice.Handler.(*chanHandler).receivedCall
	if inCall.Username != bob2.Username {
		t.Fatalf("received call from unexpected username: %s", inCall.Username)
	}
	log.Printf("Alice: received call from Bob")

	// Test adding a new PKG.
	newAddFriendConfig := *u.addFriendServer.CurrentConfig()
	newAddFriendConfig.PrevConfigHash = newAddFriendConfig.Hash()
	newAddFriendConfig.Created = time.Now()
	newPKG, err := mock.LaunchPKG(u.CoordinatorKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	log.Printf("Created new PKG server: %s", newPKG.Address)
	newAddFriendConfig.PKGServers = append(newAddFriendConfig.PKGServers, newPKG.PublicServerConfig)
	newConfigURL := fmt.Sprintf("https://%s/addfriend/newconfig", u.CoordinatorAddress)
	resp, err := (&edhttp.Client{}).PostJSON(u.CoordinatorKey, newConfigURL, newAddFriendConfig)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := ioutil.ReadAll(resp.Body)
		t.Fatalf("bad http status code %s: %s: %q", newConfigURL, resp.Status, msg)
	}
	log.Printf("Uploaded new addfriend config")

	time.Sleep(2 * time.Second)

	_, err = bob2.SendFriendRequest(alice.Username, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-bob2.Handler.(*chanHandler).sentFriendRequest

	friendRequest = <-alice.Handler.(*chanHandler).receivedFriendRequest
	// No guarantee that Verifiers will be in the same order but it works for now:
	if !reflect.DeepEqual(friendRequest.Verifiers, newAddFriendConfig.PKGServers) {
		t.Fatalf("unexpected verifiers:\ngot:  %#v\nwant: %#v", friendRequest.Verifiers, newAddFriendConfig.PKGServers)
	}
	log.Printf("Alice: received friend request from Bob")

	_, err = friendRequest.Approve()
	if err != nil {
		t.Fatal(err)
	}
	<-alice.Handler.(*chanHandler).sentFriendRequest
	<-alice.Handler.(*chanHandler).confirmedFriend

	friend = <-bob2.Handler.(*chanHandler).confirmedFriend
	friend.Call(1)
	outCall = <-bob2.Handler.(*chanHandler).sentCall
	if outCall.Intent != 1 {
		t.Fatalf("wrong intent: got %d, want %d", outCall.Intent, 1)
	}
	log.Printf("Bob: confirmed friend; calling with intent 1")

	inCall = <-alice.Handler.(*chanHandler).receivedCall
	if inCall.Intent != 1 {
		t.Fatalf("wrong intent: got %d, want %d", inCall.Intent, 1)
	}
	log.Printf("Alice: received call with intent 1")
}

type universe struct {
	Dir string

	CDN      *mock.CDN
	Mixchain *mock.Mixchain
	PKGs     []*mock.PKG

	CDNKey        ed25519.PublicKey
	cdnServer     *cdn.Server
	cdnHTTPServer *http.Server

	CoordinatorAddress    string
	CoordinatorKey        ed25519.PublicKey
	dialingServer         *coordinator.Server
	addFriendServer       *coordinator.Server
	coordinatorHTTPServer *http.Server
}

func (u *universe) Destroy() error {
	// TODO close everything else
	return os.RemoveAll(u.Dir)
}

func createAlpenhornUniverse() *universe {
	var err error

	u := new(universe)

	u.Dir, err = ioutil.TempDir("", "alpenhorn_universe_")
	if err != nil {
		log.Panicf("ioutil.TempDir: %s", err)
	}

	coordinatorPublic, coordinatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	u.CoordinatorKey = coordinatorPublic

	u.CDN = mock.LaunchCDN(u.Dir, coordinatorPublic)

	u.Mixchain = mock.LaunchMixchain(3, u.CDN.Addr, coordinatorPublic, u.CDN.PublicKey)

	u.PKGs = make([]*mock.PKG, 3)
	for i := range u.PKGs {
		srv, err := mock.LaunchPKG(coordinatorPublic, nil)
		if err != nil {
			log.Panicf("launching PKG: %s", err)
		}
		u.PKGs[i] = srv
	}

	addFriendConfig := &coordinator.AlpenhornConfig{
		Service: "AddFriend",
		Created: time.Now(),
		Expires: time.Now().Add(24 * time.Hour),

		PKGServers: make([]pkg.PublicServerConfig, len(u.PKGs)),
		MixServers: u.Mixchain.Servers,
		CDNServer: coordinator.CDNServerConfig{
			Key:     u.CDN.PublicKey,
			Address: u.CDN.Addr,
		},
	}
	for i, pkgServer := range u.PKGs {
		addFriendConfig.PKGServers[i] = pkgServer.PublicServerConfig
	}

	u.addFriendServer = &coordinator.Server{
		Service:    "AddFriend",
		PrivateKey: coordinatorPrivate,

		PKGWait:      1 * time.Second,
		MixWait:      1 * time.Second,
		RoundWait:    2 * time.Second,
		NumMailboxes: 1,

		PersistPath: filepath.Join(u.Dir, "addfriend-coordinator-state"),
	}

	if err := u.addFriendServer.Bootstrap(addFriendConfig); err != nil {
		log.Panicf("bootstrapping addfriend server: %s", err)
	}
	if err := u.addFriendServer.Run(); err != nil {
		log.Panicf("starting addfriend loop: %s", err)
	}

	dialingConfig := *addFriendConfig
	dialingConfig.Service = "Dialing"
	u.dialingServer = &coordinator.Server{
		Service:    "Dialing",
		PrivateKey: coordinatorPrivate,

		MixWait:      1 * time.Second,
		RoundWait:    2 * time.Second,
		NumMailboxes: 1,

		PersistPath: filepath.Join(u.Dir, "dialing-coordinator-state"),
	}

	if err := u.dialingServer.Bootstrap(&dialingConfig); err != nil {
		log.Panicf("bootstrapping dialing server: %s", err)
	}
	if err := u.dialingServer.Run(); err != nil {
		log.Panicf("starting dialing loop: %s", err)
	}

	coordinatorListener, err := edtls.Listen("tcp", "localhost:0", coordinatorPrivate)
	if err != nil {
		log.Panicf("edtls.Listen: %s", err)
	}
	u.CoordinatorAddress = coordinatorListener.Addr().String()

	mux := http.NewServeMux()
	mux.Handle("/addfriend/", http.StripPrefix("/addfriend", u.addFriendServer))
	mux.Handle("/dialing/", http.StripPrefix("/dialing", u.dialingServer))
	u.coordinatorHTTPServer = &http.Server{
		Handler: mux,
	}
	go func() {
		err := u.coordinatorHTTPServer.Serve(coordinatorListener)
		if err != http.ErrServerClosed {
			log.Fatalf("http.Serve: %s", err)
		}
	}()

	return u
}
