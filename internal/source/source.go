package source

import (
	"context"
	"database/sql"

	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/webhook"
)

// BlockchainPoller polls an external data source for blockchain transactions.
// Implement this interface to add support for a new data provider.
type BlockchainPoller interface {
	// Run starts the polling loop and blocks until ctx is cancelled.
	Run(ctx context.Context)
}

// Deps holds shared dependencies injected into each source factory.
type Deps struct {
	DB         *sql.DB
	Config     *config.Config
	Dispatcher *webhook.Dispatcher
}

// Factory creates a BlockchainPoller with the given dependencies.
type Factory func(deps Deps) BlockchainPoller

var registry = map[string]Factory{}

// Register adds a factory for the given source name (e.g., "blockchair").
// Call Register from main before starting pollers.
func Register(name string, f Factory) {
	registry[name] = f
}

// New creates a poller for the given source name.
// Returns nil if no factory is registered for that name.
func New(name string, deps Deps) BlockchainPoller {
	f, ok := registry[name]
	if !ok {
		return nil
	}
	return f(deps)
}

// KnownSources returns all registered source names.
func KnownSources() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}
