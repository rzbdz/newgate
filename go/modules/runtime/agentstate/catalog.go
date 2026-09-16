package agentstate

import (
	"sync"

	"github.com/rzbdz/newgate/go/modules/contracts"
)

var (
	mu           sync.RWMutex
	catalog      contracts.AgentCatalog
	currentOwner *owner
)

type owner struct{}

func Set(next contracts.AgentCatalog) func() {
	mu.Lock()
	previous := catalog
	previousOwner := currentOwner
	token := &owner{}
	catalog = next
	currentOwner = token
	mu.Unlock()
	return func() {
		mu.Lock()
		if currentOwner == token {
			catalog = previous
			currentOwner = previousOwner
		}
		mu.Unlock()
	}
}

func Catalog() contracts.AgentCatalog {
	mu.RLock()
	current := catalog
	mu.RUnlock()
	if current == nil {
		panic("runtime: agent catalog capability is unavailable")
	}
	return current
}
