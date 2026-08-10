package tuisession

// IdentifierSource supplies unique, bounded identifiers for client mutations
// and input messages. Implementations must be safe for concurrent use.
type IdentifierSource interface {
	Next() (string, error)
}
