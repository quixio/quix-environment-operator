package namespace

import "errors"

// Sentinel errors for namespace management failures. They are wrapped (via %w) into the
// descriptive errors returned by the manager so callers can classify failures with errors.Is
// instead of fragile substring matching on the message text.
var (
	// ErrNamespaceNotManaged indicates a namespace exists but is not managed by this operator.
	ErrNamespaceNotManaged = errors.New("not managed by this operator")
	// ErrNamespaceOwnershipConflict indicates a managed namespace belongs to a different Environment.
	ErrNamespaceOwnershipConflict = errors.New("owned by a different environment")
	// ErrNamespaceEnvironmentIDMismatch indicates the namespace environment-id does not match the Environment.
	ErrNamespaceEnvironmentIDMismatch = errors.New("environment-id mismatch")
)
