package main

import (
	"net/http"
	"testing"
)

func TestCanonicalChatIdentityProjectConversation(t *testing.T) {
	key, canonical, provider, project, conversation, err := canonicalChatIdentity("https://chatgpt.com/g/g-p-abc/c/1234?foo=bar#x")
	if err != nil {
		t.Fatal(err)
	}
	if key != "chat://chatgpt/g-p-abc/1234" || canonical != "https://chatgpt.com/g/g-p-abc/c/1234" || provider != "chatgpt" || project != "g-p-abc" || conversation != "1234" {
		t.Fatalf("unexpected identity: %q %q %q %q %q", key, canonical, provider, project, conversation)
	}
}

func TestCanonicalChatIdentityRejectsNonConversation(t *testing.T) {
	if _, _, _, _, _, err := canonicalChatIdentity("https://chatgpt.com/"); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestLocatorRegisterResolve(t *testing.T) {
	cp := newTestControlPlane(t)
	loc := ChatLocation{CanonicalURL: "https://chatgpt.com/g/g-p-abc/c/1234", TabActorID: "actor-1", TabID: 17, WindowID: 8}
	if _, err := cp.saveChatLocation(loc); err != nil {
		t.Fatal(err)
	}
	found := cp.localLocations("chat://chatgpt/g-p-abc/1234")
	if len(found) != 1 || found[0].TabActorID != "actor-1" {
		t.Fatalf("unexpected locations: %#v", found)
	}
}

func TestPeerRequestSignatureRoundTrip(t *testing.T) {
	req, err := http.NewRequest("GET", "http://127.0.0.1:9791/peer/resolve?chatKey=chat%3A%2F%2Fchatgpt%2Fx", nil)
	if err != nil {
		t.Fatal(err)
	}
	signPeerRequest(req, nil, "secret")
	if err := verifyPeerRequest(req, nil, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := verifyPeerRequest(req, nil, "wrong"); err == nil {
		t.Fatal("wrong secret should fail")
	}
}
